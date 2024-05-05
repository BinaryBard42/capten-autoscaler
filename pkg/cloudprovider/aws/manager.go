/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

//go:generate go run ec2_instance_types/gen.go -region $AWS_REGION

package aws

import (
	"errors"
	"fmt"
	"strings"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	operationWaitTimeout    = 5 * time.Second
	operationPollInterval   = 100 * time.Millisecond
	maxRecordsReturnedByAPI = 100
	maxAsgNamesPerDescribe  = 100
	refreshInterval         = 1 * time.Minute
	autoDiscovererTypeASG   = "asg"
	asgAutoDiscovererKeyTag = "tag"
	optionsTagsPrefix       = "k8s.io/cluster-autoscaler/node-template/autoscaling-options/"
	labelAwsCSITopologyZone = "topology.ebs.csi.aws.com/zone"
)

// AwsManager is handles aws communication and data caching.
type AwsManager struct {
	awsService    awsWrapper
	asgCache      *asgCache
	lastRefresh   time.Time
	instanceTypes map[string]*InstanceType
}

type asgTemplate struct {
	InstanceType *InstanceType
	Region       string
	Zone         string
	Tags         []string
}

// createAwsManagerInternal allows for custom objects to be passed in by tests
func createAWSManagerInternal(
	awsService *awsWrapper,
	instanceTypes map[string]*InstanceType,
) (*AwsManager, error) {

	cache, err := newASGCache(awsService, []string{})
	if err != nil {
		return nil, err
	}

	manager := &AwsManager{
		awsService:    *awsService,
		asgCache:      cache,
		instanceTypes: instanceTypes,
	}

	if err := manager.forceRefresh(); err != nil {
		return nil, err
	}

	return manager, nil
}

// Refresh is called before every main loop and can be used to dynamically update cloud provider state.
// In particular the list of node groups returned by NodeGroups can change as a result of CloudProvider.Refresh().
func (m *AwsManager) Refresh() error {
	if m.lastRefresh.Add(refreshInterval).After(time.Now()) {
		return nil
	}
	return m.forceRefresh()
}

func (m *AwsManager) forceRefresh() error {
	if err := m.asgCache.regenerate(); err != nil {
		klog.Errorf("Failed to regenerate ASG cache: %v", err)
		return err
	}
	m.lastRefresh = time.Now()
	klog.V(2).Infof("Refreshed ASG list, next refresh after %v", m.lastRefresh.Add(refreshInterval))
	return nil
}

// GetAsgForInstance returns AsgConfig of the given Instance
func (m *AwsManager) GetAsgForInstance(instance AwsInstanceRef) *asg {
	return m.asgCache.FindForInstance(instance)
}

// Cleanup the ASG cache.
func (m *AwsManager) Cleanup() {
	m.asgCache.Cleanup()
}

func (m *AwsManager) getAsgs() map[AwsRef]*asg {
	return m.asgCache.Get()
}

func (m *AwsManager) getAutoscalingOptions(ref AwsRef) map[string]string {
	return m.asgCache.GetAutoscalingOptions(ref)
}

// SetAsgSize sets ASG size.
func (m *AwsManager) SetAsgSize(asg *asg, size int) error {
	return m.asgCache.SetAsgSize(asg, size)
}

// DeleteInstances deletes the given instances. All instances must be controlled by the same ASG.
func (m *AwsManager) DeleteInstances(instances []*AwsInstanceRef) error {
	if err := m.asgCache.DeleteInstances(instances); err != nil {
		return err
	}
	klog.V(2).Infof("DeleteInstances was called: scheduling an ASG list refresh for next main loop evaluation")
	m.lastRefresh = time.Now().Add(-refreshInterval)
	return nil
}

// GetAsgNodes returns Asg nodes.
func (m *AwsManager) GetAsgNodes(ref AwsRef) ([]AwsInstanceRef, error) {
	return m.asgCache.InstancesByAsg(ref)
}

// GetInstanceStatus returns the status of ASG nodes
func (m *AwsManager) GetInstanceStatus(ref AwsInstanceRef) (*string, error) {
	return m.asgCache.InstanceStatus(ref)
}

func (m *AwsManager) getAsgTemplate(asg *asg) (*asgTemplate, error) {
	if len(asg.AvailabilityZones) < 1 {
		return nil, fmt.Errorf("unable to get first AvailabilityZone for ASG %q", asg.Name)
	}

	az := asg.AvailabilityZones[0]
	region := az[0 : len(az)-1]

	if len(asg.AvailabilityZones) > 1 {
		klog.V(4).Infof("Found multiple availability zones for ASG %q; using %s for %s label\n", asg.Name, az, apiv1.LabelZoneFailureDomain)
	}

	instanceTypeName, err := getInstanceTypeForAsg(m.asgCache, asg)
	if err != nil {
		return nil, err
	}

	if t, ok := m.instanceTypes[instanceTypeName]; ok {
		return &asgTemplate{
			InstanceType: t,
			Region:       region,
			Zone:         az,
		}, nil
	}

	return nil, fmt.Errorf("ASG %q uses the unknown EC2 instance type %q", asg.Name, instanceTypeName)
}

func (m *AwsManager) updateCapacityWithRequirementsOverrides(capacity *apiv1.ResourceList, policy *mixedInstancesPolicy) error {
	if policy == nil || len(policy.instanceTypesOverrides) > 0 {
		return nil
	}

	return nil
}

// An asgAutoDiscoveryConfig specifies how to autodiscover AWS ASGs.
type asgAutoDiscoveryConfig struct {
	// Tags to match on.
	// Any ASG with all of the provided tag keys will be autoscaled.
	Tags map[string]string
}

func parseASGAutoDiscoverySpec(spec string) (asgAutoDiscoveryConfig, error) {
	cfg := asgAutoDiscoveryConfig{}

	tokens := strings.SplitN(spec, ":", 2)
	if len(tokens) != 2 {
		return cfg, fmt.Errorf("invalid node group auto discovery spec specified via --node-group-auto-discovery: %s", spec)
	}
	discoverer := tokens[0]
	if discoverer != autoDiscovererTypeASG {
		return cfg, fmt.Errorf("unsupported discoverer specified: %s", discoverer)
	}
	param := tokens[1]
	kv := strings.SplitN(param, "=", 2)
	if len(kv) != 2 {
		return cfg, fmt.Errorf("invalid key=value pair %s", kv)
	}
	k, v := kv[0], kv[1]
	if k != asgAutoDiscovererKeyTag {
		return cfg, fmt.Errorf("unsupported parameter key \"%s\" is specified for discoverer \"%s\". The only supported key is \"%s\"", k, discoverer, asgAutoDiscovererKeyTag)
	}
	if v == "" {
		return cfg, errors.New("tag value not supplied")
	}
	p := strings.Split(v, ",")
	if len(p) == 0 {
		return cfg, fmt.Errorf("invalid ASG tag for auto discovery specified: ASG tag must not be empty")
	}
	cfg.Tags = make(map[string]string, len(p))
	for _, label := range p {
		lp := strings.SplitN(label, "=", 2)
		if len(lp) > 1 {
			cfg.Tags[lp[0]] = lp[1]
			continue
		}
		cfg.Tags[lp[0]] = ""
	}
	return cfg, nil
}
