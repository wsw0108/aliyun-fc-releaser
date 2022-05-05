package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aliyun/fc-go-sdk"
	"github.com/blang/semver/v4"
	"github.com/denverdino/aliyungo/common"
	"github.com/denverdino/aliyungo/ros/standard"
	"github.com/wsw0108/aliyun-fc-releaser/internal/serverless"
	"github.com/wsw0108/aliyun-fc-releaser/internal/types"
	"gopkg.in/yaml.v3"
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
		configFile     string
		templateFile   string
		releaseVersion string
		instances      int64
		noProvision    bool
		stackName      string
		regionID       string
	)
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalln(err)
	}
	defaultConfigFile := filepath.Join(home, ".fcli", "config.yaml")
	flag.StringVar(&configFile, "c", defaultConfigFile, "config file of funcraft")
	flag.StringVar(&templateFile, "t", "template.yml", "template.yml to use")
	flag.StringVar(&releaseVersion, "r", "", "release version")
	flag.Int64Var(&instances, "instances", 0, "number of instances")
	flag.BoolVar(&noProvision, "no-provision", false, "do not create provision")
	flag.StringVar(&stackName, "stack-name", "", "ros stack name")
	flag.StringVar(&regionID, "region", "", "region name, default value will be extracted from endpoint")
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

	if regionID == "" {
		regionID = extractRegion(config.Endpoint)
	}
	if stackName != "" && regionID == "" {
		log.Println("region required when using ros(-stack-name)")
		os.Exit(-1)
	}

	if releaseVersion == "" {
		log.Println("release version required")
		os.Exit(-1)
	}
	if releaseVersion[0] == 'v' {
		releaseVersion = releaseVersion[1:]
	}

	var template serverless.Template
	tf, err := os.Open(templateFile)
	if err != nil {
		log.Fatalln(err)
	}
	defer tf.Close()
	err = yaml.NewDecoder(tf).Decode(&template)
	if err != nil {
		log.Fatalln(err)
	}

	ctx := &Context{
		stackName: stackName,
		regionID:  regionID,
	}

	ctx.fcClient, err = fc.NewClient(config.Endpoint, config.ApiVersion, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		log.Fatalln(err)
	}

	if stackName != "" {
		ctx.rosClient = standard.NewROSClient(config.AccessKeyID, config.AccessKeySecret, common.Region(regionID))
	}

	// NOTE: 1.2.3/v1.2.3 -> v1_2_3, （字母开头，字母数字下划线中划线）
	ver := semver.MustParse(releaseVersion)
	var aliasName string
	aliasName = strings.ReplaceAll(releaseVersion, ".", "_")
	if aliasName[0] != 'v' {
		aliasName = "v" + aliasName
	}
	if len(ver.Pre) > 0 {
		aliasName = aliasName + "-pre"
		ctx.snapshot = true
		ctx.prevQualifier = fmt.Sprintf("v%d_%d_%d", ver.Major, ver.Minor, ver.Patch)
	}

	var services []serverless.Service
	var customDomains []serverless.CustomDomain

	for _, service := range template.Services {
		serviceName, err := ctx.GetServiceName(service.Name)
		if err != nil {
			log.Fatalln(err)
		}
		service.Name = serviceName
		services = append(services, service)
	}

	req := fc.NewListCustomDomainsInput()
	resp, err := ctx.fcClient.ListCustomDomains(req)
	if err != nil {
		log.Fatalln(err)
	}
	for _, customDomain := range template.CustomDomains {
		cdc := customDomain
		cdc.RouteConfig.Routes = make([]serverless.PathConfig, 0, len(customDomain.RouteConfig.Routes))
		for _, route := range customDomain.RouteConfig.Routes {
			serviceName, err := ctx.GetServiceName(route.ServiceName)
			if err != nil {
				log.Fatalln(err)
			}
			route.ServiceName = serviceName
			cdc.RouteConfig.Routes = append(cdc.RouteConfig.Routes, route)
		}
		domainName := customDomain.DomainName
		if domainName == "Auto" {
			for _, tplRoute := range cdc.RouteConfig.Routes {
				serviceName := tplRoute.ServiceName
				functionName := tplRoute.FunctionName
				for _, cdr := range resp.CustomDomains {
					if strings.HasSuffix(*cdr.DomainName, ".test.functioncompute.com") {
						for _, route := range cdr.RouteConfig.Routes {
							if *route.ServiceName == serviceName && *route.FunctionName == functionName {
								domainName = *cdr.DomainName
							}
						}
					}
				}
			}
		}
		if domainName == "Auto" {
			panic("can not handle 'DomainName: Auto'")
		}
		cdc.DomainName = domainName
		customDomains = append(customDomains, cdc)
	}

	for _, service := range services {
		if _, err = PublishAndCreateAlias(ctx, service.Name, releaseVersion, aliasName); err != nil {
			log.Fatalln(err)
		}
		for _, function := range service.Functions {
			for _, trigger := range function.Triggers {
				if trigger.Type != "HTTP" {
					continue
				}
				if err = CreateHttpTrigger(ctx, service.Name, function.Name, trigger, releaseVersion, aliasName); err != nil {
					log.Fatalln(err)
				}
			}
		}
	}
	for _, customDomain := range customDomains {
		if err = UpdateCustomDomain(ctx, customDomain, aliasName); err != nil {
			log.Fatalln(err)
		}
	}
	if !noProvision && !ctx.snapshot {
		for _, service := range services {
			for _, function := range service.Functions {
				if err = CreateProvisionConfig(ctx, service.Name, aliasName, function.Name, instances); err != nil {
					log.Fatalln(err)
				}
			}
		}
	}
}

func PublishAndCreateAlias(ctx *Context, serviceName string, releaseVersion string, aliasName string) (string, error) {
	publishServiceVersionInput := fc.NewPublishServiceVersionInput(serviceName)
	publishServiceVersionInput.WithDescription(releaseVersion)
	publishServiceVersionOutput, err := ctx.fcClient.PublishServiceVersion(publishServiceVersionInput)
	// NOTE: "can not publish version for service 'xxx', detail: 'No changes were made since last publish'"
	if err != nil {
		return "", err
	}
	createAliasInput := fc.NewCreateAliasInput(serviceName)
	createAliasInput.WithVersionID(*publishServiceVersionOutput.VersionID)
	createAliasInput.WithAliasName(aliasName)
	createAliasInput.WithDescription(releaseVersion)
	createAliasOutput, err := ctx.fcClient.CreateAlias(createAliasInput)
	if err != nil {
		return "", err
	}
	// TODO: 同时创建相应的ROS资源？
	return *createAliasOutput.VersionID, nil
}

func CreateHttpTrigger(ctx *Context, serviceName string, functionName string, trigger serverless.Trigger, releaseVersion string, qualifier string) error {
	listTriggerInput := fc.NewListTriggersInput(serviceName, functionName)
	listTriggerOutput, err := ctx.fcClient.ListTriggers(listTriggerInput)
	if err != nil {
		return err
	}
	if len(listTriggerOutput.Triggers) >= types.MaxTriggers {
		var triggers types.Triggers
		for _, tm := range listTriggerOutput.Triggers {
			createTime, _ := time.Parse(types.TimeLayout, *tm.CreatedTime)
			modifyTime, _ := time.Parse(types.TimeLayout, *tm.LastModifiedTime)
			tt := types.Trigger{
				CreateTime: createTime,
				ModifyTime: modifyTime,
				Name:       *tm.TriggerName,
			}
			if tm.Qualifier != nil {
				tt.Qualifier = *tm.Qualifier
			}
			triggers = append(triggers, tt)
		}
		sort.Sort(triggers)
		triggersToDelete := triggers[:(len(triggers) - (types.MaxTriggers - 1))]
		for _, td := range triggersToDelete {
			deleteTriggerInput := fc.NewDeleteTriggerInput(serviceName, functionName, td.Name)
			_, err = ctx.fcClient.DeleteTrigger(deleteTriggerInput)
			if err != nil {
				return err
			}
			if td.Qualifier != "" && td.Qualifier != "LATEST" {
				// TODO: remove resources(route/alias/version) related to qualifier?
			}
		}
	}
	createTriggerInput := fc.NewCreateTriggerInput(serviceName, functionName)
	// NOTE: 一个版本qualifier只能创建一个触发器
	createTriggerInput.WithQualifier(qualifier)
	triggerName := fmt.Sprintf("%s-%s", trigger.Name, qualifier)
	createTriggerInput.WithTriggerName(triggerName)
	createTriggerInput.WithTriggerType("http")
	triggerConfig := fc.NewHTTPTriggerConfig()
	triggerConfig.WithAuthType(strings.ToLower(trigger.HTTP.AuthType))
	triggerConfig.WithMethods(trigger.HTTP.Methods...)
	createTriggerInput.WithTriggerConfig(triggerConfig)
	createTriggerInput.WithDescription(releaseVersion)
	_, err = ctx.fcClient.CreateTrigger(createTriggerInput)
	// TODO: 同时创建相应的ROS资源？
	return err
}

func UpdateCustomDomain(ctx *Context, customDomain serverless.CustomDomain, qualifier string) error {
	listCustomDomainInput := fc.NewListCustomDomainsInput()
	listCustomDomainOutput, err := ctx.fcClient.ListCustomDomains(listCustomDomainInput)
	var routeConfigToUpdate *fc.RouteConfig
	for _, d := range listCustomDomainOutput.CustomDomains {
		if *d.DomainName == customDomain.DomainName {
			routeConfigToUpdate = d.RouteConfig
			break
		}
	}
	if routeConfigToUpdate == nil {
		return errors.New("can not find custom domain to update routes")
	}
	updateCustomDomainInput := fc.NewUpdateCustomDomainInput(customDomain.DomainName)
	updateCustomDomainInput.WithProtocol(customDomain.Protocol)
	routeConfig := fc.NewRouteConfig()
	// TODO: 最多只保留固定数量的Routes（依限制而定）
	for _, route := range routeConfigToUpdate.Routes {
		// 非ROS，fun deploy直接用template中的覆盖
		// ROS，fun deploy不改变路由设置
		if ctx.snapshot {
			// FIXME: prevQualifier不存在的话不需要加（能够添加）
			// route.WithQualifier(ctx.prevQualifier)
		} else {
			for _, froute := range customDomain.RouteConfig.Routes {
				if *route.Path == froute.Path && *route.ServiceName == froute.ServiceName && *route.FunctionName == froute.FunctionName {
					route.WithQualifier(qualifier)
					break
				}
			}
		}
		routeConfig.Routes = append(routeConfig.Routes, route)
	}
	if ctx.snapshot {
		for _, route := range customDomain.RouteConfig.Routes {
			newRoute := fc.PathConfig{}
			prefix := "/" + qualifier
			path := prefix + route.Path
			newRoute.Path = &path
			newRoute.ServiceName = &route.ServiceName
			newRoute.FunctionName = &route.FunctionName
			newRoute.Qualifier = &qualifier
			// newRoute.Methods = route.Methods
			routeConfig.Routes = append(routeConfig.Routes, newRoute)
		}
	}
	updateCustomDomainInput.WithRouteConfig(routeConfig)
	_, err = ctx.fcClient.UpdateCustomDomain(updateCustomDomainInput)
	return err
}

func CreateProvisionConfig(ctx *Context, serviceName string, qualifier string, functionName string, targetInstances int64) error {
	listProvisionConfigsInput := fc.NewListProvisionConfigsInput()
	listProvisionConfigsOutput, err := ctx.fcClient.ListProvisionConfigs(listProvisionConfigsInput)
	if err != nil {
		return err
	}
	putProvisionConfigInput := fc.NewPutProvisionConfigInput(serviceName, qualifier, functionName)
	putProvisionConfigInput.WithTarget(targetInstances)
	_, err = ctx.fcClient.PutProvisionConfig(putProvisionConfigInput)
	if err != nil {
		return err
	}
	// TODO: 同时创建相应ROS资源
	resourcePattern := fmt.Sprintf("^.*#%s#(.+)#%s$", serviceName, functionName)
	resourceRegex := regexp.MustCompile(resourcePattern)
	var qualifiers []string
	for _, pc := range listProvisionConfigsOutput.ProvisionConfigs {
		if pc.Resource == nil || pc.Current == nil || pc.Target == nil {
			continue
		}
		if *pc.Current == 0 && *pc.Target == 0 {
			continue
		}
		matches := resourceRegex.FindStringSubmatch(*pc.Resource)
		if len(matches) < 2 {
			continue
		}
		qualifiers = append(qualifiers, matches[1])
	}
	if len(qualifiers) > 0 {
		for _, qualifierToUpdate := range qualifiers {
			updateProvisionConfigInput := fc.NewPutProvisionConfigInput(serviceName, qualifierToUpdate, functionName)
			updateProvisionConfigInput.WithTarget(0)
			// ignore error
			_, _ = ctx.fcClient.PutProvisionConfig(updateProvisionConfigInput)
		}
	}
	return nil
}

type Context struct {
	stackName string
	regionID  string

	fcClient  *fc.Client
	rosClient *standard.Client

	snapshot      bool
	prevQualifier string

	mu      sync.Mutex
	stackID string
}

func (ctx *Context) getStackID() (string, error) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.stackID == "" {
		resp, err := ctx.rosClient.ListStacks(&standard.ListStacksRequest{
			StackName: []string{ctx.stackName},
		})
		if err != nil {
			return "", err
		}
		// FIXME: ListStacks does not filter out stacks by specified stackName
		var found bool
		for _, stack := range resp.Stacks {
			if stack.StackName == ctx.stackName {
				ctx.stackID = stack.StackId
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("can not get StackID for StackName: %s", ctx.stackName)
		}
	}
	return ctx.stackID, nil
}

func (ctx *Context) GetServiceName(serviceName string) (string, error) {
	if ctx.stackName == "" {
		return serviceName, nil
	}
	ctx.getStackID()
	req := &standard.GetStackResourceRequest{
		StackId:                ctx.stackID,
		LogicalResourceId:      serviceName,
		ShowResourceAttributes: true,
	}
	res := &GetStackResourceResponse{}
	err := ctx.rosClient.Invoke("GetStackResource", req, res)
	if err != nil {
		return "", err
	}
	var rosServiceName string
	for _, attr := range res.ResourceAttributes {
		if attr.ResourceAttributeKey == "ServiceName" {
			rosServiceName = attr.ResourceAttributeValue.(string)
			break
		}
	}
	if rosServiceName == "" {
		return "", fmt.Errorf("can not get ROS ServiceName for service %s", serviceName)
	}
	// TODO: cache rosServiceName for serviceName
	return rosServiceName, nil
}

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

func extractRegion(endpoint string) string {
	re := "^https?:\\/\\/[^.]+\\.([^.]+)\\..+$"
	regex, err := regexp.Compile(re)
	if err != nil {
		return ""
	}
	matches := regex.FindStringSubmatch(endpoint)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}
