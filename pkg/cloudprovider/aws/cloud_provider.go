package aws

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	klog "k8s.io/klog/v2"
)

const (
	// GPULabel is the label added to nodes with GPU resource.
	GPULabel = "k8s.amazonaws.com/accelerator"
	// nodeNotPresentErr indicates no node with the given identifier present in AWS
	nodeNotPresentErr = "node is not present in aws"
)

var (
	availableGPUTypes = map[string]struct{}{
		"nvidia-tesla-k80":  {},
		"nvidia-tesla-p100": {},
		"nvidia-tesla-v100": {},
		"nvidia-tesla-t4":   {},
		"nvidia-tesla-a100": {},
		"nvidia-a10g":       {},
	}
)

// awsCloudProvider implements CloudProvider interface.
type awsCloudProvider struct {
	awsManager *AwsManager
}

// Cleanup stops the go routine that is handling the current view of the ASGs in the form of a cache
func (aws *awsCloudProvider) Cleanup() error {
	aws.awsManager.Cleanup()
	return nil
}

// GPULabel returns the label added to nodes with GPU resource.
func (aws *awsCloudProvider) GPULabel() string {
	return GPULabel
}

// GetAvailableGPUTypes return all available GPU types cloud provider supports
func (aws *awsCloudProvider) GetAvailableGPUTypes() map[string]struct{} {
	return availableGPUTypes
}

// NodeGroups returns all node groups configured for this cloud provider.
func (aws *awsCloudProvider) NodeGroups() []*AwsNodeGroup {
	asgs := aws.awsManager.getAsgs()
	ngs := make([]*AwsNodeGroup, 0, len(asgs))
	for _, asg := range asgs {
		ngs = append(ngs, &AwsNodeGroup{
			asg:        asg,
			awsManager: aws.awsManager,
		})
	}

	return ngs
}

// NodeGroupForNode returns the node group for the given node.
func (aws *awsCloudProvider) NodeGroupForNode(node *apiv1.Node) (*AwsNodeGroup, error) {
	if len(node.Spec.ProviderID) == 0 {
		klog.Warningf("Node %v has no providerId", node.Name)
		return nil, nil
	}
	ref, err := AwsRefFromProviderId(node.Spec.ProviderID)
	if err != nil {
		return nil, err
	}
	asg := aws.awsManager.GetAsgForInstance(*ref)

	if asg == nil {
		return nil, nil
	}

	return &AwsNodeGroup{
		asg:        asg,
		awsManager: aws.awsManager,
	}, nil
}

// HasInstance returns whether a given node has a corresponding instance in this cloud provider
func (aws *awsCloudProvider) HasInstance(node *apiv1.Node) (bool, error) {
	// we haven't implemented a way to check if a fargate instance
	// exists in the cloud provider
	// returning 'true' because we are assuming the node exists in AWS
	// this is the default behavior if the check is unimplemented
	if strings.HasPrefix(node.GetName(), "fargate") {
		return true, errors.New("not Implemented")
	}

	// avoid log spam for not autoscaled asgs:
	//   Nodes that belong to an asg that is not autoscaled will not be found in the asgCache below,
	//   so do not trigger warning spam by returning an error from being unable to find them.
	//   Annotation is not automated, but users that see the warning can add the annotation to avoid it.
	if node.Annotations != nil && node.Annotations["k8s.io/cluster-autoscaler/enabled"] == "false" {
		return false, nil
	}

	awsRef, err := AwsRefFromProviderId(node.Spec.ProviderID)
	if err != nil {
		return false, err
	}

	// we don't care about the status
	status, err := aws.awsManager.asgCache.InstanceStatus(*awsRef)
	if status != nil {
		return true, nil
	}

	return false, fmt.Errorf("%s: %v", nodeNotPresentErr, err)
}

// GetAvailableMachineTypes get all machine types that can be requested from the cloud provider.
func (aws *awsCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	return []string{}, nil
}

// Refresh is called before every main loop and can be used to dynamically update cloud provider state.
// In particular the list of node groups returned by NodeGroups can change as a result of CloudProvider.Refresh().
func (aws *awsCloudProvider) Refresh() error {
	return aws.awsManager.Refresh()
}

// AwsRef contains a reference to some entity in AWS world.
type AwsRef struct {
	Name string
}

// AwsInstanceRef contains a reference to an instance in the AWS world.
type AwsInstanceRef struct {
	ProviderID string
	Name       string
}

var validAwsRefIdRegex = regexp.MustCompile(fmt.Sprintf(`^aws\:\/\/\/[-0-9a-z]*\/[-0-9a-z]*(\/[-0-9a-z\.]*)?$|aws\:\/\/\/[-0-9a-z]*\/%s.*$`, placeholderInstanceNamePrefix))

// AwsRefFromProviderId creates AwsInstanceRef object from provider id which
// must be in format: aws:///zone/name
func AwsRefFromProviderId(id string) (*AwsInstanceRef, error) {
	if validAwsRefIdRegex.FindStringSubmatch(id) == nil {
		return nil, fmt.Errorf("wrong id: expected format aws:///<zone>/<name>, got %v", id)
	}
	splitted := strings.Split(id[7:], "/")
	return &AwsInstanceRef{
		ProviderID: id,
		Name:       splitted[1],
	}, nil
}

// AwsNodeGroup implements NodeGroup interface.
type AwsNodeGroup struct {
	awsManager *AwsManager
	asg        *asg
}

// MaxSize returns maximum size of the node group.
func (ng *AwsNodeGroup) MaxSize() int {
	return ng.asg.maxSize
}

// MinSize returns minimum size of the node group.
func (ng *AwsNodeGroup) MinSize() int {
	return ng.asg.minSize
}

// TargetSize returns the current TARGET size of the node group. It is possible that the
// number is different from the number of nodes registered in Kubernetes.
func (ng *AwsNodeGroup) TargetSize() (int, error) {
	return ng.asg.curSize, nil
}

// Exist checks if the node group really exists on the cloud provider side. Allows to tell the
// theoretical node group from the real one.
func (ng *AwsNodeGroup) Exist() bool {
	return true
}

// Autoprovisioned returns true if the node group is autoprovisioned.
func (ng *AwsNodeGroup) Autoprovisioned() bool {
	return false
}

// Delete deletes the node group on the cloud provider side.
// This will be executed only for autoprovisioned node groups, once their size drops to 0.
func (ng *AwsNodeGroup) Delete() error {
	return nil
}

// IncreaseSize increases Asg size
func (ng *AwsNodeGroup) IncreaseSize(delta int) error {
	if delta <= 0 {
		return fmt.Errorf("size increase must be positive")
	}
	size := ng.asg.curSize
	if size+delta > ng.asg.maxSize {
		return fmt.Errorf("size increase too large - desired:%d max:%d", size+delta, ng.asg.maxSize)
	}
	return ng.awsManager.SetAsgSize(ng.asg, size+delta)
}

// DecreaseTargetSize decreases the target size of the node group. This function
// doesn't permit to delete any existing node and can be used only to reduce the
// request for new nodes that have not been yet fulfilled. Delta should be negative.
// It is assumed that cloud provider will not delete the existing nodes if the size
// when there is an option to just decrease the target.
func (ng *AwsNodeGroup) DecreaseTargetSize(delta int) error {
	if delta >= 0 {
		return fmt.Errorf("size decrease size must be negative")
	}

	size := ng.asg.curSize
	nodes, err := ng.awsManager.GetAsgNodes(ng.asg.AwsRef)
	if err != nil {
		return err
	}
	if int(size)+delta < len(nodes) {
		return fmt.Errorf("attempt to delete existing nodes targetSize:%d delta:%d existingNodes: %d",
			size, delta, len(nodes))
	}
	return ng.awsManager.SetAsgSize(ng.asg, size+delta)
}

// Belongs returns true if the given node belongs to the NodeGroup.
func (ng *AwsNodeGroup) Belongs(node *apiv1.Node) (bool, error) {
	ref, err := AwsRefFromProviderId(node.Spec.ProviderID)
	if err != nil {
		return false, err
	}
	targetAsg := ng.awsManager.GetAsgForInstance(*ref)
	if targetAsg == nil {
		return false, fmt.Errorf("%s doesn't belong to a known asg", node.Name)
	}
	if targetAsg.AwsRef != ng.asg.AwsRef {
		return false, nil
	}
	return true, nil
}

// DeleteNodes deletes the nodes from the group.
func (ng *AwsNodeGroup) DeleteNodes(nodes []*apiv1.Node) error {
	size := ng.asg.curSize
	if int(size) <= ng.MinSize() {
		return fmt.Errorf("min size reached, nodes will not be deleted")
	}
	refs := make([]*AwsInstanceRef, 0, len(nodes))
	for _, node := range nodes {
		belongs, err := ng.Belongs(node)
		if err != nil {
			return err
		}
		if !belongs {
			return fmt.Errorf("%s belongs to a different asg than %s", node.Name, ng.Id())
		}
		awsref, err := AwsRefFromProviderId(node.Spec.ProviderID)
		if err != nil {
			return err
		}
		refs = append(refs, awsref)
	}
	return ng.awsManager.DeleteInstances(refs)
}

// Id returns asg id.
func (ng *AwsNodeGroup) Id() string {
	return ng.asg.Name
}

// Debug returns a debug string for the Asg.
func (ng *AwsNodeGroup) Debug() string {
	return fmt.Sprintf("%s (%d:%d)", ng.Id(), ng.MinSize(), ng.MaxSize())
}

// Nodes returns a list of all nodes that belong to this node group.
func (ng *AwsNodeGroup) Nodes() ([]AwsInstanceRef, error) {
	return ng.awsManager.GetAsgNodes(ng.asg.AwsRef)
}
