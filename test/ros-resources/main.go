package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/ros"
	"github.com/denverdino/aliyungo/common"
	ros2 "github.com/denverdino/aliyungo/ros"
	"github.com/denverdino/aliyungo/ros/standard"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Endpoint        string `yaml:"endpoint"`
	ApiVersion      string `yaml:"api_version"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	SecurityToken   string `yaml:"security_token"`
	Debug           bool   `yaml:"debug"`
	Timeout         int    `yaml:"timeout"`
	Retries         int    `yaml:"retries"`
}

func main() {
	var (
		configFile   string
		regionID     string
		stackName    string
		serviceName  string
		functionName string
		resourceName string
	)
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalln(err)
	}
	defaultConfigFile := filepath.Join(home, ".fcli", "config.yaml")
	flag.StringVar(&configFile, "c", defaultConfigFile, "config file of funcraft")
	flag.StringVar(&regionID, "region", "cn-shanghai", "region id")
	flag.StringVar(&stackName, "stack-name", "", "ros stack name to use")
	flag.StringVar(&serviceName, "service-name", "", "service name")
	flag.StringVar(&functionName, "function-name", "", "function name")
	flag.StringVar(&resourceName, "resource-name", "", "resource name")
	flag.Parse()

	var config Config
	cf, err := os.Open(configFile)
	if err != nil {
		log.Fatalln(err)
	}
	defer cf.Close()
	err = yaml.NewDecoder(cf).Decode(&config)
	if err != nil {
		log.Fatalln(err)
	}

	if stackName == "" {
		return
	}

	if resourceName == "" {
		if serviceName != "" {
			resourceName = serviceName
			if functionName != "" {
				// NOTE: ros's name rule for function resource
				resourceName = serviceName + functionName
			}
		}
	}

	var stackID string

	// ENV["DEBUG"]="sdk"
	rosClient, err := ros.NewClientWithAccessKey(regionID, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		log.Fatalln(err)
	}
	type StackMeta struct {
		Name string
		Id   string
	}
	type DescribeStacksResponse struct {
		TotalCount int
		Stacks     []StackMeta
	}
	{
		request := ros.CreateDescribeStacksRequest()
		request.Name = stackName
		// request.RegionId = regionID
		request.Headers["x-acs-region-id"] = regionID
		response, err := rosClient.DescribeStacks(request)
		if err != nil {
			log.Fatalln("DescribeStacks", err)
		}
		var res DescribeStacksResponse
		json.Unmarshal([]byte(response.GetHttpContentString()), &res)
		stackID = res.Stacks[0].Id
	}
	fmt.Println()
	{
		request := ros.CreateDescribeResourcesRequest()
		request.StackId = stackID
		request.StackName = stackName
		// request.RegionId = regionID
		request.Headers["x-acs-region-id"] = regionID
		response, _ := rosClient.DescribeResources(request)
		// if err != nil {
		// 	log.Fatalln("DescribeResources", err)
		// }
		fmt.Println("DescribeResources", response.GetHttpContentString())
	}
	fmt.Println()
	if resourceName != "" {
		request := ros.CreateDescribeResourceDetailRequest()
		request.StackId = stackID
		request.StackName = stackName
		request.ResourceName = resourceName
		// request.RegionId = regionID
		request.Headers["x-acs-region-id"] = regionID
		response, err := rosClient.DescribeResourceDetail(request)
		if err != nil {
			log.Fatalln("DescribeResourceDetail", err)
		}
		if response != nil {
			fmt.Println("DescribeResourceDetail", response.GetHttpContentString())
		}
	}
	fmt.Println()

	fmt.Println(strings.Repeat("-", 50))
	// APIVersion 2019-09-10
	rosClient1 := standard.NewROSClient(config.AccessKeyID, config.AccessKeySecret, common.Region(regionID))
	rosClient1.SetDebug(true)
	{
		resp, err := rosClient1.ListStacks(&standard.ListStacksRequest{
			StackName: []string{stackName},
		})
		if err != nil {
			log.Fatalln(err)
		}
		// FIXME: ListStacks does not filter out stacks by specified stackName
		var found bool
		for _, stack := range resp.Stacks {
			if stack.StackName == stackName {
				stackID = stack.StackId
				found = true
				fmt.Println("rosClient1, ListStacks", stack)
				break
			}
		}
		if !found {
			stackID = resp.Stacks[0].StackId
		}
	}
	{
		stack, err := rosClient1.GetStack(&standard.GetStackRequest{
			StackId: stackID,
		})
		if err != nil {
			log.Fatalln(err)
		}
		fmt.Println("rosClient1, GetStack", stack)
	}
	if resourceName != "" {
		type ResourceAttribute struct {
			ResourceAttributeValue interface{}
			ResourceAttributeKey   string
		}
		type GetStackResourceResponse struct {
			Status            string
			Description       string
			LogicalResourceId string
			StackId           string

			StackName           string
			StatusReason        string
			PhysicalResourceId  string
			ResourceType        string
			CreateTime          string
			Metadata            map[string]string
			UpdateTime          string
			ResourceAttributes  []ResourceAttribute
			RequestId           string
			DriftDetectionTime  string
			ResourceDriftStatus string
		}
		req := &standard.GetStackResourceRequest{
			StackId:                stackID,
			LogicalResourceId:      resourceName,
			ShowResourceAttributes: true,
		}
		res := &GetStackResourceResponse{}
		err = rosClient1.Invoke("GetStackResource", req, res)
		if err != nil {
			log.Fatalln(err)
		}
		fmt.Println("rosClient1, GetStackResource", res)
	}

	fmt.Println(strings.Repeat("-", 50))
	// APIVersion 2015-09-01
	rosClient2 := ros2.NewClient(config.AccessKeyID, config.AccessKeySecret)
	rosClient2.SetDebug(true)
	{
		resp, err := rosClient2.DescribeStacks(&ros2.DescribeStacksRequest{
			RegionId: common.Region(regionID),
			Name:     stackName,
		})
		if err != nil {
			log.Fatalln(err)
		}
		fmt.Println("rosClient2, DescribeStacks", resp)
		stackID = resp.Stacks[0].Id
	}
	{
		resources, err := rosClient2.DescribeResources(stackID, stackName)
		if err != nil {
			log.Fatalln(err)
		}
		for _, resource := range resources {
			fmt.Println("rosClient2, DescribeResources", resource)
		}
	}
	if resourceName != "" {
		resp, err := rosClient2.DescribeResource(stackID, stackName, resourceName)
		if err != nil {
			log.Fatalln(err)
		}
		fmt.Println("rosClient2, DescribeResource", resp)
	}
}
