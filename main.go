package main

import (
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

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ros "github.com/alibabacloud-go/ros-20190910/v4/client"
	"github.com/aliyun/fc-go-sdk"
	"github.com/blang/semver/v4"
	"github.com/wsw0108/aliyun-fc-releaser/internal/serverless"
	"github.com/wsw0108/aliyun-fc-releaser/internal/types"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Endpoint        string `yaml:"endpoint,omitempty"`
	RegionID        string `yaml:"region_id,omitempty"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
}

func loadConfig(configFile string) (*Config, error) {
	f, err := os.Open(configFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var decoded Config
	err = yaml.NewDecoder(f).Decode(&decoded)
	if err != nil {
		return nil, err
	}
	return &decoded, nil
}

func main() {
	var (
		configFile     string
		templateFile   string
		releaseVersion string
		instances      int64
		stackName      string
		regionID       string
		dryRun         bool
	)
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalln(err)
	}
	flag.StringVar(&configFile, "c", "", "config file contains credentials to release to fc")
	flag.StringVar(&templateFile, "t", "template.yml", "template.yml to use")
	flag.StringVar(&releaseVersion, "r", "", "release version")
	flag.Int64Var(&instances, "instances", 0, "number of instances")
	flag.StringVar(&stackName, "stack-name", "", "ros stack name")
	flag.StringVar(&regionID, "region", "", "region name, default value will be extracted from endpoint")
	flag.BoolVar(&dryRun, "dry-run", false, "do not perform real update")
	flag.Parse()

	funConfigFile := filepath.Join(home, ".fcli", "config.yaml")
	configFiles := []string{configFile, funConfigFile}

	var config *Config
	var useConfigFile string
	for _, filename := range configFiles {
		if filename == "" {
			continue
		}
		decoded, err1 := loadConfig(filename)
		if err1 != nil {
			log.Println(err1)
			continue
		}
		config = decoded
		useConfigFile = filename
		break
	}
	if config == nil {
		log.Fatalln("can not read config file")
	}
	log.Println("Using config file", useConfigFile)

	if regionID == "" {
		regionID = config.RegionID
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
		dryRun:    dryRun,
		stackName: stackName,
		regionID:  regionID,
	}

	{
		client, err := fc.NewClient(config.Endpoint, "2016-08-15", config.AccessKeyID, config.AccessKeySecret)
		if err != nil {
			log.Fatalln(err)
		}
		ctx.fcClient = client
	}

	if stackName != "" {
		apiConfig := openapi.Config{}
		apiConfig.SetAccessKeyId(config.AccessKeyID)
		apiConfig.SetAccessKeySecret(config.AccessKeySecret)
		client, err := ros.NewClient(&apiConfig)
		if err != nil {
			log.Fatalln(err)
		}
		ctx.rosClient = client
	}

	// NOTE: 1.2.3/v1.2.3 -> v1_2_3, （字母开头，字母数字下划线中划线）
	ver := semver.MustParse(releaseVersion)
	var aliasName string
	aliasName = strings.ReplaceAll(releaseVersion, ".", "_")
	if aliasName[0] != 'v' {
		aliasName = "v" + aliasName
	}
	if len(ver.Pre) > 0 {
		aliasName = aliasName + "_" + time.Now().Format("2006_01_02")
		ctx.snapshot = true
		ctx.prevQualifier = fmt.Sprintf("v%d_%d_%d", ver.Major, ver.Minor, ver.Patch)
	}

	var services []serverless.Service
	var customDomains []serverless.CustomDomain

	for _, service := range template.Services {
		serviceName, err1 := ctx.GetServiceName(service.Name)
		if err1 != nil {
			log.Fatalln(err1)
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
			serviceName, err1 := ctx.GetServiceName(route.ServiceName)
			if err1 != nil {
				log.Fatalln(err1)
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
		log.Printf("Publish version and alias for service %s", service.Name)
		if _, err = PublishAndCreateAlias(ctx, service.Name, releaseVersion, aliasName); err != nil {
			log.Fatalln(err)
		}
		for _, function := range service.Functions {
			log.Printf("Create HTTP Triggers for function %s", function.Name)
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
	if !ctx.snapshot && instances > 0 {
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
	listServiceVersionsInput := fc.NewListServiceVersionsInput(serviceName)
	published := false
	var publishedVersionID string
	{
		resp, err := ctx.fcClient.ListServiceVersions(listServiceVersionsInput)
		if err != nil {
			return "", err
		}
		log.Println("Existing versions:")
		for _, vm := range resp.Versions {
			log.Printf("  id: %s, description: %s", *vm.VersionID, *vm.Description)
		}
		for _, vm := range resp.Versions {
			if vm.Description == nil {
				continue
			}
			if *vm.Description == releaseVersion {
				published = true
				publishedVersionID = *vm.VersionID
				break
			}
		}
	}
	if published {
		log.Printf("Version %s[%s] for service %s already published", releaseVersion, publishedVersionID, serviceName)
	} else {
		log.Printf("Version %s for service %s will be published", releaseVersion, serviceName)
	}
	listAliasInput := fc.NewListAliasesInput(serviceName)
	aliasExists := false
	var aliasVersionID string
	{
		resp, err := ctx.fcClient.ListAliases(listAliasInput)
		if err != nil {
			return "", err
		}
		log.Println("Existing alias:")
		for _, am := range resp.Aliases {
			log.Printf("  name: %s, id: %s, description: %s", *am.AliasName, *am.VersionID, *am.Description)
		}
		for _, am := range resp.Aliases {
			if am.AliasName == nil {
				continue
			}
			if *am.AliasName == aliasName {
				aliasExists = true
				aliasVersionID = *am.VersionID
				break
			}
		}
	}
	if aliasExists {
		log.Printf("Alias %s for version %s[%s] of service %s already exists", aliasName, releaseVersion, aliasVersionID, serviceName)
	} else {
		log.Printf("Alias %s for version %s of service %s will be created", aliasName, releaseVersion, serviceName)
	}
	if ctx.dryRun {
		return "", nil
	}
	if !published {
		publishServiceVersionInput := fc.NewPublishServiceVersionInput(serviceName)
		publishServiceVersionInput.WithDescription(releaseVersion)
		publishServiceVersionOutput, err := ctx.fcClient.PublishServiceVersion(publishServiceVersionInput)
		// NOTE: "can not publish version for service 'xxx', detail: 'No changes were made since last publish'"
		if err != nil {
			return "", err
		}
		publishedVersionID = *publishServiceVersionOutput.VersionID
	}
	if !aliasExists {
		createAliasInput := fc.NewCreateAliasInput(serviceName)
		createAliasInput.WithVersionID(publishedVersionID)
		createAliasInput.WithAliasName(aliasName)
		createAliasInput.WithDescription(releaseVersion)
		_, err := ctx.fcClient.CreateAlias(createAliasInput)
		if err != nil {
			return "", err
		}
	}
	// TODO: 同时创建相应的ROS资源？
	return publishedVersionID, nil
}

func CreateHttpTrigger(ctx *Context, serviceName string, functionName string, trigger serverless.Trigger, releaseVersion string, qualifier string) error {
	triggerName := fmt.Sprintf("%s-%s", trigger.Name, qualifier)
	listTriggerInput := fc.NewListTriggersInput(serviceName, functionName)
	listTriggerOutput, err := ctx.fcClient.ListTriggers(listTriggerInput)
	if err != nil {
		return err
	}
	triggerExists := false
	var triggers types.Triggers
	for _, tm := range listTriggerOutput.Triggers {
		if *tm.TriggerName == triggerName {
			triggerExists = true
		}
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
	log.Printf("Existing Triggers:")
	for _, tm := range triggers {
		log.Printf("  name: %s, qualifier [%s]", tm.Name, tm.Qualifier)
	}
	if triggerExists {
		log.Printf("Trigger %s already exists", triggerName)
		return nil
	}
	if len(triggers) >= types.MaxTriggers {
		triggersToDelete := triggers[:(len(triggers) - (types.MaxTriggers - 1))]
		log.Printf("Triggers to delete:")
		for _, td := range triggersToDelete {
			log.Printf("  name %s, qualifier [%s]", td.Name, td.Qualifier)
			if !ctx.dryRun {
				deleteTriggerInput := fc.NewDeleteTriggerInput(serviceName, functionName, td.Name)
				_, err = ctx.fcClient.DeleteTrigger(deleteTriggerInput)
			}
			if err != nil {
				return err
			}
			if td.Qualifier != "" && td.Qualifier != "LATEST" {
				// TODO: remove resources(route/alias/version) related to qualifier?
			}
		}
	}
	log.Printf("Create Trigger:")
	log.Printf("  name %s, qualifier [%s]", triggerName, qualifier)
	if ctx.dryRun {
		return nil
	}
	createTriggerInput := fc.NewCreateTriggerInput(serviceName, functionName)
	// NOTE: 一个版本qualifier只能创建一个触发器
	createTriggerInput.WithQualifier(qualifier)
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
		log.Printf("Name of Custom Domain to create: %s", customDomain.DomainName)
		createCustomDomainInput := fc.NewCreateCustomDomainInput()
		createCustomDomainInput.DomainName = &customDomain.DomainName
		createCustomDomainInput.Protocol = &customDomain.Protocol
		routeConfig := fc.NewRouteConfig()
		for _, route := range customDomain.RouteConfig.Routes {
			newRoute := fc.PathConfig{}
			newRoute.Path = &route.Path
			newRoute.ServiceName = &route.ServiceName
			newRoute.FunctionName = &route.FunctionName
			newRoute.Qualifier = &qualifier
			// newRoute.Methods = route.Methods
			routeConfig.Routes = append(routeConfig.Routes, newRoute)
		}
		createCustomDomainInput.RouteConfig = routeConfig
		certConfig := fc.CertConfig{}
		certConfig.CertName = &customDomain.CertConfig.CertName
		certConfig.Certificate = &customDomain.CertConfig.Certificate
		certConfig.PrivateKey = &customDomain.CertConfig.PrivateKey
		createCustomDomainInput.CertConfig = &certConfig
		log.Printf("Routes of Custom Domain:")
		for _, route := range routeConfig.Routes {
			if route.Qualifier != nil {
				log.Printf("  service %s, function %s, path %s, qualifier [%s]", *route.ServiceName, *route.FunctionName, *route.Path, *route.Qualifier)
			} else {
				log.Printf("  service %s, function %s, path %s, qualifier [%s]", *route.ServiceName, *route.FunctionName, *route.Path, "")
			}
		}
		if !ctx.dryRun {
			_, err = ctx.fcClient.CreateCustomDomain(createCustomDomainInput)
		}
		return err
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
	routeExistsInConfig := func(routeConfig *fc.RouteConfig, route *fc.PathConfig) bool {
		for _, r := range routeConfig.Routes {
			if *r.ServiceName == *route.ServiceName && *r.FunctionName == *route.FunctionName && *r.Path == *route.Path && *r.Qualifier == *route.Qualifier {
				return true
			}
		}
		return false
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
			if !routeExistsInConfig(routeConfig, &newRoute) {
				routeConfig.Routes = append(routeConfig.Routes, newRoute)
			}
		}
	}
	updateCustomDomainInput.WithRouteConfig(routeConfig)
	log.Printf("Name of Custom Domain to update: %s", customDomain.DomainName)
	log.Printf("Routes of Custom Domain to update:")
	for _, route := range routeConfig.Routes {
		if route.Qualifier != nil {
			log.Printf("  service %s, function %s, path %s, qualifier [%s]", *route.ServiceName, *route.FunctionName, *route.Path, *route.Qualifier)
		} else {
			log.Printf("  service %s, function %s, path %s, qualifier [%s]", *route.ServiceName, *route.FunctionName, *route.Path, "")
		}
	}
	if !ctx.dryRun {
		_, err = ctx.fcClient.UpdateCustomDomain(updateCustomDomainInput)
	}
	return err
}

func CreateProvisionConfig(ctx *Context, serviceName string, qualifier string, functionName string, targetInstances int64) error {
	listProvisionConfigsInput := fc.NewListProvisionConfigsInput()
	listProvisionConfigsOutput, err := ctx.fcClient.ListProvisionConfigs(listProvisionConfigsInput)
	if err != nil {
		return err
	}
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
	if ctx.dryRun {
		log.Println("Existing provision configs:")
		for _, pc := range listProvisionConfigsOutput.ProvisionConfigs {
			fmt.Println(*pc)
		}
		log.Println("Qualifiers to update:")
		for _, s := range qualifiers {
			fmt.Println(s)
		}
		return nil
	}
	putProvisionConfigInput := fc.NewPutProvisionConfigInput(serviceName, qualifier, functionName)
	putProvisionConfigInput.WithTarget(targetInstances)
	_, err = ctx.fcClient.PutProvisionConfig(putProvisionConfigInput)
	if err != nil {
		return err
	}
	// TODO: 同时创建相应ROS资源
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
	dryRun bool

	stackName string
	regionID  string

	fcClient  *fc.Client
	rosClient *ros.Client

	snapshot      bool
	prevQualifier string

	mu      sync.Mutex
	stackID string
}

func (ctx *Context) getStackID() (string, error) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.stackID == "" {
		resp, err := ctx.rosClient.ListStacks(&ros.ListStacksRequest{
			StackName: []*string{&ctx.stackName},
			RegionId:  &ctx.regionID,
		})
		if err != nil {
			return "", err
		}
		// FIXME: ListStacks does not filter out stacks by specified stackName
		var found bool
		for _, stack := range resp.Body.Stacks {
			if *stack.StackName == ctx.stackName {
				ctx.stackID = *stack.StackId
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
	req := ros.GetStackResourceRequest{
		StackId:           &ctx.stackID,
		RegionId:          &ctx.regionID,
		LogicalResourceId: &serviceName,
	}
	req.SetShowResourceAttributes(true)
	res, err := ctx.rosClient.GetStackResource(&req)
	if err != nil {
		return "", err
	}
	var rosServiceName string
	for _, attr := range res.Body.ResourceAttributes {
		if _, ok := attr["ResourceAttributeKey"]; !ok {
			continue
		}
		if _, ok := attr["ResourceAttributeValue"]; !ok {
			continue
		}
		key := attr["ResourceAttributeKey"].(string)
		if key == "ServiceName" {
			rosServiceName = attr["ResourceAttributeValue"].(string)
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
