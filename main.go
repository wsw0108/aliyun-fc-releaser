package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/aliyun/fc-go-sdk"
	"github.com/denverdino/aliyungo/common"
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
		configFile     string
		templateFile   string
		releaseVersion string
		instances      int64
		versionedRoute bool
		noProvision    bool
		stackName      string
		regionID       string
		serviceName    string
		functionName   string
	)
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalln(err)
	}
	defaultConfigFile := filepath.Join(home, ".fcli", "config.yaml")
	flag.StringVar(&configFile, "c", defaultConfigFile, "config file of funcraft")
	flag.StringVar(&templateFile, "t", "template.yml", "template.yml to use")
	flag.StringVar(&releaseVersion, "r", "", "release version")
	flag.Int64Var(&instances, "instances", 1, "number of instances")
	flag.BoolVar(&versionedRoute, "versioned-route", false, "create versioned route")
	flag.BoolVar(&noProvision, "no-provision", false, "do not create provision")
	flag.StringVar(&stackName, "stack-name", "", "ros stack name")
	flag.StringVar(&regionID, "region", "", "region name, default value will be extracted from endpoint")
	flag.StringVar(&serviceName, "service-name", "", "service name")
	flag.StringVar(&functionName, "function-name", "", "function name")
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

	var template map[string]interface{}
	tf, err := os.Open(templateFile)
	if err != nil {
		log.Fatalln(err)
	}
	defer tf.Close()
	err = yaml.NewDecoder(tf).Decode(&template)
	if err != nil {
		log.Fatalln(err)
	}

	fcClient, err := fc.NewClient(config.Endpoint, config.ApiVersion, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		log.Fatalln(err)
	}

	var rosClient *standard.Client
	if stackName != "" {
		rosClient = standard.NewROSClient(config.AccessKeyID, config.AccessKeySecret, common.Region(regionID))
	}

	// NOTE: 1.2.3/v1.2.3 -> v1_2_3, （字母开头，字母数字下划线中划线）
	var aliasName string
	aliasName = strings.ReplaceAll(releaseVersion, ".", "_")
	if aliasName[0] != 'v' {
		aliasName = "v" + aliasName
	}

	// qualifier -> versionPrefix
	parts := strings.Split(aliasName, "_")
	versionPrefix := "/" + parts[0]

	parseCtx := &ParseContext{
		fcClien: fcClient,

		ROSClient: rosClient,
		StackName: stackName,
		RegionID:  regionID,

		versionedRoute: versionedRoute,
		versionPrefix:  versionPrefix,
	}

	{
		if serviceName != "" && functionName != "" {
			if _, err = PublishAndCreateAlias(fcClient, serviceName, releaseVersion, aliasName); err != nil {
				log.Fatalln("CreateAlias", err)
			}
			listTriggerInput := fc.NewListTriggersInput(serviceName, functionName)
			listTriggerOutput, err := fcClient.ListTriggers(listTriggerInput)
			if err != nil {
				log.Fatalln("ListTriggers", err)
			}
			fmt.Println("ListTrigger", listTriggerOutput)
			var triggerCreated bool
			for _, trigger := range listTriggerOutput.Triggers {
				if trigger.TriggerType == nil || trigger.TriggerConfig == nil {
					continue
				}
				triggerType := *trigger.TriggerType
				if !strings.EqualFold(triggerType, "http") {
					continue
				}
				if trigger.Qualifier != nil {
					continue
				}
				fcHttpTrigger := trigger.TriggerConfig.(*fc.HTTPTriggerConfig)
				triggerName := fmt.Sprintf("%s-%s", *trigger.TriggerName, aliasName)
				httpTrigger := &HttpTrigger{
					Name:     triggerName,
					AuthType: *fcHttpTrigger.AuthType,
					Methods:  fcHttpTrigger.Methods,
				}
				if err = CreateHttpTrigger(fcClient, serviceName, functionName, httpTrigger, releaseVersion, aliasName); err != nil {
					log.Fatalln("CreateTrigger", err)
				}
				triggerCreated = true
			}
			if !triggerCreated {
				return
			}
			listCustomDomainInput := fc.NewListCustomDomainsInput()
			listCustomDomainOutput, err := fcClient.ListCustomDomains(listCustomDomainInput)
			fmt.Println("ListCustomDomain", listCustomDomainOutput)
			for _, customDomain := range listCustomDomainOutput.CustomDomains {
				if customDomain.RouteConfig == nil || len(customDomain.RouteConfig.Routes) == 0 {
					continue
				}
				fmt.Println("CustomDomain", customDomain)
				routeConfig := fc.NewRouteConfig()
				var hasMatch bool
				for _, route := range customDomain.RouteConfig.Routes {
					if *route.ServiceName == serviceName && *route.FunctionName == functionName {
						hasMatch = true
						pathConfig := fc.PathConfig{}
						pathConfig.Path = route.Path
						pathConfig.ServiceName = route.ServiceName
						pathConfig.FunctionName = route.FunctionName
						pathConfig.Methods = route.Methods
						pathConfig.Qualifier = &aliasName
						routeConfig.Routes = append(routeConfig.Routes, pathConfig)
					}
				}
				if !hasMatch {
					fmt.Println("no match service/function", *customDomain.DomainName)
					continue
				}
				updateCustomDomainInput := fc.NewUpdateCustomDomainInput(*customDomain.DomainName)
				updateCustomDomainInput.Protocol = customDomain.Protocol
				updateCustomDomainInput.RouteConfig = routeConfig
				if customDomain.CertConfig != nil {
					if customDomain.CertConfig.CertName != nil || customDomain.CertConfig.Certificate != nil || customDomain.CertConfig.PrivateKey != nil {
						updateCustomDomainInput.CertConfig = customDomain.CertConfig
					}
				}
				fmt.Println("updateCustomDomainInput", updateCustomDomainInput)
				updateCustomDomainOutput, err := fcClient.UpdateCustomDomain(updateCustomDomainInput)
				if err != nil {
					log.Fatalln("UpdateCustomDomain", err)
				}
				fmt.Println("UpdateCustomDomain", updateCustomDomainOutput)
			}
			if !noProvision {
				if err = CreateProvisionConfig(fcClient, serviceName, aliasName, functionName, instances); err != nil {
					log.Fatalln("CreateProvisionConfig", err)
				}
			}
		} else {
			resources := template["Resources"].(map[interface{}]interface{})
			var services []*Service
			var customDomains []*CustomDomain
			for key, value := range resources {
				name := key.(string)
				if props, ok := value.(map[interface{}]interface{}); ok {
					typV := props["Type"]
					if typV == nil {
						continue
					}
					typ := typV.(string)
					if typ == "Aliyun::Serverless::Service" {
						service := parseService(parseCtx, name, props)
						services = append(services, service)
					} else if typ == "Aliyun::Serverless::CustomDomain" {
						customDomain := parseCustomDomain(parseCtx, name, props)
						customDomains = append(customDomains, customDomain)
					}
				}
			}
			for _, service := range services {
				if _, err = PublishAndCreateAlias(fcClient, service.Name, releaseVersion, aliasName); err != nil {
					log.Fatalln(err)
				}
				for _, function := range service.Functions {
					for _, trigger := range function.Triggers {
						if err = CreateHttpTrigger(fcClient, service.Name, function.Name, trigger, releaseVersion, aliasName); err != nil {
							log.Fatalln(err)
						}
					}
				}
			}
			for _, customDomain := range customDomains {
				if err = UpdateCustomDomain(fcClient, customDomain, aliasName); err != nil {
					log.Fatalln(err)
				}
			}
			if !noProvision {
				for _, service := range services {
					for _, function := range service.Functions {
						if err = CreateProvisionConfig(fcClient, service.Name, aliasName, function.Name, instances); err != nil {
							log.Fatalln(err)
						}
					}
				}
			}
		}
	}
}

func PublishAndCreateAlias(client *fc.Client, serviceName string, releaseVersion string, aliasName string) (string, error) {
	publishServiceVersionInput := fc.NewPublishServiceVersionInput(serviceName)
	publishServiceVersionInput.WithDescription(releaseVersion)
	publishServiceVersionOutput, err := client.PublishServiceVersion(publishServiceVersionInput)
	// NOTE: "can not publish version for service 'xxx', detail: 'No changes were made since last publish'"
	if err != nil {
		return "", err
	}
	createAliasInput := fc.NewCreateAliasInput(serviceName)
	createAliasInput.WithVersionID(*publishServiceVersionOutput.VersionID)
	createAliasInput.WithAliasName(aliasName)
	createAliasInput.WithDescription(releaseVersion)
	createAliasOutput, err := client.CreateAlias(createAliasInput)
	if err != nil {
		return "", err
	}
	return *createAliasOutput.VersionID, nil
}

func CreateHttpTrigger(client *fc.Client, serviceName string, functionName string, trigger *HttpTrigger, releaseVersion string, qualifier string) error {
	// FIXME: 一个函数下最多创建10个触发器
	createTriggerInput := fc.NewCreateTriggerInput(serviceName, functionName)
	// NOTE: 一个版本qualifier只能创建一个触发器
	createTriggerInput.WithQualifier(qualifier)
	triggerName := fmt.Sprintf("%s-%s", trigger.Name, qualifier)
	createTriggerInput.WithTriggerName(triggerName)
	createTriggerInput.WithTriggerType("http")
	triggerConfig := fc.NewHTTPTriggerConfig()
	triggerConfig.WithAuthType(trigger.AuthType)
	triggerConfig.WithMethods(trigger.Methods...)
	createTriggerInput.WithTriggerConfig(triggerConfig)
	createTriggerInput.WithDescription(releaseVersion)
	_, err := client.CreateTrigger(createTriggerInput)
	return err
}

func UpdateCustomDomain(client *fc.Client, customDomain *CustomDomain, qualifier string) error {
	updateCustomDomainInput := fc.NewUpdateCustomDomainInput(customDomain.DomainName)
	updateCustomDomainInput.WithProtocol(customDomain.Protocol)
	routeConfig := fc.NewRouteConfig()
	for _, route := range customDomain.RouteConfig.Routes {
		fcPathConfig := fc.PathConfig{}
		fcPathConfig.WithPath(route.Path)
		fcPathConfig.WithServiceName(route.ServiceName)
		fcPathConfig.WithFunctionName(route.FunctionName)
		if route.versioned {
			fcPathConfig.WithQualifier(qualifier)
		}
		routeConfig.Routes = append(routeConfig.Routes, fcPathConfig)
	}
	updateCustomDomainInput.WithRouteConfig(routeConfig)
	// if customDomain.CertConfig != nil {
	// 	if customDomain.CertConfig.CertName != nil || customDomain.CertConfig.Certificate != nil || customDomain.CertConfig.PrivateKey != nil {
	// 		updateCustomDomainInput.CertConfig = customDomain.CertConfig
	// 	}
	// }
	_, err := client.UpdateCustomDomain(updateCustomDomainInput)
	return err
}

func CreateProvisionConfig(client *fc.Client, serviceName string, qualifier string, functionName string, targetInstances int64) error {
	// TODO: 删除以前的版本的预留资源设置？
	putProvisionConfigInput := fc.NewPutProvisionConfigInput(serviceName, qualifier, functionName)
	putProvisionConfigInput.WithTarget(targetInstances)
	_, err := client.PutProvisionConfig(putProvisionConfigInput)
	return err
}

type HttpTrigger struct {
	Name     string
	AuthType string
	Methods  []string
}

type Function struct {
	Name     string
	Triggers []*HttpTrigger
}

type Service struct {
	Name      string
	Functions []*Function
}

type ParseContext struct {
	fcClien *fc.Client

	ROSClient *standard.Client
	StackName string
	RegionID  string

	versionedRoute bool
	versionPrefix  string

	mu      sync.Mutex
	stackID string
}

func (ctx *ParseContext) getStackID() (string, error) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.stackID == "" {
		resp, err := ctx.ROSClient.ListStacks(&standard.ListStacksRequest{
			StackName: []string{ctx.StackName},
		})
		if err != nil {
			return "", err
		}
		// FIXME: ListStacks does not filter out stacks by specified stackName
		var found bool
		for _, stack := range resp.Stacks {
			if stack.StackName == ctx.StackName {
				ctx.stackID = stack.StackId
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("can not get StackID for StackName: %s", ctx.StackName)
		}
	}
	return ctx.stackID, nil
}

func (ctx *ParseContext) GetServiceName(serviceName string) (string, error) {
	if ctx.StackName == "" {
		return serviceName, nil
	}
	ctx.getStackID()
	req := &standard.GetStackResourceRequest{
		StackId:                ctx.stackID,
		LogicalResourceId:      serviceName,
		ShowResourceAttributes: true,
	}
	res := &GetStackResourceResponse{}
	err := ctx.ROSClient.Invoke("GetStackResource", req, res)
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
	return rosServiceName, nil
}

func parseService(ctx *ParseContext, serviceName string, values map[interface{}]interface{}) *Service {
	serviceName, err := ctx.GetServiceName(serviceName)
	if err != nil {
		panic(err)
	}
	var functions []*Function
	for key, value := range values {
		functionName := key.(string)
		if props, ok := value.(map[interface{}]interface{}); ok {
			typV := props["Type"]
			if typV == nil {
				continue
			}
			typ := typV.(string)
			if typ == "Aliyun::Serverless::Function" {
				function := parseFunction(ctx, functionName, props)
				functions = append(functions, function)
			}
		}
	}
	return &Service{
		Name:      serviceName,
		Functions: functions,
	}
}

func parseFunction(ctx *ParseContext, functionName string, values map[interface{}]interface{}) *Function {
	var triggers []*HttpTrigger
	events := values["Events"].(map[interface{}]interface{})
	for key, value := range events {
		triggerName := key.(string)
		props := value.(map[interface{}]interface{})
		typ := props["Type"].(string)
		if !strings.EqualFold(typ, "http") {
			continue
		}
		trigger := parseHttpTrigger(ctx, triggerName, props["Properties"].(map[interface{}]interface{}))
		triggers = append(triggers, trigger)
	}
	return &Function{
		Name:     functionName,
		Triggers: triggers,
	}
}

func parseHttpTrigger(ctx *ParseContext, triggerName string, props map[interface{}]interface{}) *HttpTrigger {
	authType := props["AuthType"].(string)
	values := props["Methods"].([]interface{})
	var methods []string
	for _, value := range values {
		methods = append(methods, value.(string))
	}
	return &HttpTrigger{
		Name:     triggerName,
		AuthType: strings.ToLower(authType),
		Methods:  methods,
	}
}

type RouteConfig struct {
	Routes []*PathConfig
}

type PathConfig struct {
	versioned    bool
	Path         string
	ServiceName  string
	FunctionName string
}

type CustomDomain struct {
	ResourceName string
	DomainName   string
	Protocol     string
	RouteConfig  *RouteConfig
}

func parseCustomDomain(ctx *ParseContext, resourceName string, values map[interface{}]interface{}) *CustomDomain {
	props := values["Properties"].(map[interface{}]interface{})
	domainName := props["DomainName"].(string)
	protocol := props["Protocol"].(string)
	routeConfigMap := props["RouteConfig"].(map[interface{}]interface{})
	routesMap := routeConfigMap["Routes"].(map[interface{}]interface{})
	if domainName == "Auto" {
		for _, value := range routesMap {
			props := value.(map[interface{}]interface{})
			serviceName := props["ServiceName"].(string)
			serviceName, err := ctx.GetServiceName(serviceName)
			if err != nil {
				panic(err)
			}
			functionName := props["FunctionName"].(string)
			req := fc.NewListCustomDomainsInput()
			resp, err := ctx.fcClien.ListCustomDomains(req)
			if err != nil {
				log.Fatalln(err)
			}
			for _, customDomain := range resp.CustomDomains {
				if strings.HasSuffix(*customDomain.DomainName, ".test.functioncompute.com") {
					for _, route := range customDomain.RouteConfig.Routes {
						if *route.ServiceName == serviceName && *route.FunctionName == functionName {
							domainName = *customDomain.DomainName
						}
					}
				}
			}
		}
	}
	if domainName == "Auto" {
		panic("can not handle 'DomainName: Auto'")
	}
	routeConfig := &RouteConfig{}
	for key, value := range routesMap {
		path := key.(string)
		pathConfigs := parsePathConfig(ctx, path, value.(map[interface{}]interface{}))
		routeConfig.Routes = append(routeConfig.Routes, pathConfigs...)
	}
	return &CustomDomain{
		RouteConfig:  routeConfig,
		ResourceName: resourceName,
		DomainName:   domainName,
		Protocol:     protocol,
	}
}

func parsePathConfig(ctx *ParseContext, path string, values map[interface{}]interface{}) []*PathConfig {
	serviceName := values["ServiceName"].(string)
	serviceName, err := ctx.GetServiceName(serviceName)
	if err != nil {
		panic(err)
	}
	functionName := values["FunctionName"].(string)
	var pathConfigs []*PathConfig
	if ctx.versionedRoute {
		pathConfigs = append(pathConfigs, &PathConfig{
			Path:         path,
			ServiceName:  serviceName,
			FunctionName: functionName,
		})
		versiondedPath := fmt.Sprintf("%s%s", ctx.versionPrefix, path)
		pathConfigs = append(pathConfigs, &PathConfig{
			versioned:    true,
			Path:         versiondedPath,
			ServiceName:  serviceName,
			FunctionName: functionName,
		})
	} else {
		pathConfigs = append(pathConfigs, &PathConfig{
			versioned:    true,
			Path:         path,
			ServiceName:  serviceName,
			FunctionName: functionName,
		})
	}
	return pathConfigs
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
