/*
Copyright 2019 The Kubernetes Authors.

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

package aws

import (
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
)

var (
	ec2MetaDataServiceUrl = "http://169.254.169.254"
)

// GenerateEC2InstanceTypes returns a map of ec2 resources
func GenerateEC2InstanceTypes(sess *session.Session) (map[string]*InstanceType, error) {
	instanceTypes := make(map[string]*InstanceType)

	if len(instanceTypes) == 0 {
		return nil, errors.New("unable to load EC2 Instance Type list")
	}

	return instanceTypes, nil
}

// GetStaticEC2InstanceTypes return pregenerated ec2 instance type list
func GetStaticEC2InstanceTypes() (map[string]*InstanceType, string) {
	return InstanceTypes, StaticListLastUpdateTime
}

func interpretEc2SupportedArchitecure(archName string) string {
	switch archName {
	case "arm64":
		return "arm64"
	case "i386":
		return "amd64"
	case "x86_64":
		return "amd64"
	case "x86_64_mac":
		return "amd64"
	default:
		return "amd64"
	}
}

// GetCurrentAwsRegion return region of current cluster without building awsManager
func GetCurrentAwsRegion() (string, error) {
	region, present := os.LookupEnv("AWS_REGION")

	if !present {
		c := aws.NewConfig().
			WithEndpoint(ec2MetaDataServiceUrl)
		sess, err := session.NewSession()
		if err != nil {
			return "", fmt.Errorf("failed to create session")
		}
		return ec2metadata.New(sess, c).Region()
	}

	return region, nil
}
