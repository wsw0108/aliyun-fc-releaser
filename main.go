package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aliyun/fc-go-sdk"
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

	client, err := fc.NewClient(config.Endpoint, config.ApiVersion, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		log.Fatalln(err)
	}
	aliasName := releaseVersion
	{
		if serviceName != "" && functionName != "" {
			if _, err = PublishAndCreateAlias(client, serviceName, releaseVersion, aliasName); err != nil {
				log.Fatalln("CreateAlias", err)
			}
			listTriggerInput := fc.NewListTriggersInput(serviceName, functionName)
			listTriggerOutput, err := client.ListTriggers(listTriggerInput)
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
				if err = CreateHttpTrigger(client, serviceName, functionName, httpTrigger, releaseVersion, aliasName); err != nil {
					log.Fatalln("CreateTrigger", err)
				}
				triggerCreated = true
			}
			if !triggerCreated {
				return
			}
			listCustomDomainInput := fc.NewListCustomDomainsInput()
			listCustomDomainOutput, err := client.ListCustomDomains(listCustomDomainInput)
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
				updateCustomDomainOutput, err := client.UpdateCustomDomain(updateCustomDomainInput)
				if err != nil {
					log.Fatalln("UpdateCustomDomain", err)
				}
				fmt.Println("UpdateCustomDomain", updateCustomDomainOutput)
			}
			if err = CreateProvisionConfig(client, serviceName, aliasName, functionName, 1); err != nil {
				log.Fatalln("CreateProvisionConfig", err)
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
						// FIXME: service name on aliyun fc was generated by some rules
						service := parseService(name, props)
						services = append(services, service)
					} else if typ == "Aliyun::Serverless::CustomDomain" {
						customDomain := parseCustomDomain(name, props)
						customDomains = append(customDomains, customDomain)
					}
				}
			}
			for _, service := range services {
				if _, err = PublishAndCreateAlias(client, service.Name, releaseVersion, aliasName); err != nil {
					log.Fatalln(err)
				}
				for _, function := range service.Functions {
					for _, trigger := range function.Triggers {
						if err = CreateHttpTrigger(client, service.Name, function.Name, trigger, releaseVersion, aliasName); err != nil {
							log.Fatalln(err)
						}
					}
				}
			}
			for _, customDomain := range customDomains {
				if err = UpdateCustomDomain(client, customDomain, aliasName); err != nil {
					log.Fatalln(err)
				}
			}
			for _, service := range services {
				for _, function := range service.Functions {
					if err = CreateProvisionConfig(client, service.Name, aliasName, function.Name, 1); err != nil {
						log.Fatalln(err)
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
		fcPathConfig.WithQualifier(qualifier)
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

func parseService(name string, values map[interface{}]interface{}) *Service {
	var functions []*Function
	for key, value := range values {
		name := key.(string)
		if props, ok := value.(map[interface{}]interface{}); ok {
			typV := props["Type"]
			if typV == nil {
				continue
			}
			typ := typV.(string)
			if typ == "Aliyun::Serverless::Function" {
				function := parseFunction(name, props)
				functions = append(functions, function)
			}
		}
	}
	return &Service{
		Name:      name,
		Functions: functions,
	}
}

func parseFunction(name string, values map[interface{}]interface{}) *Function {
	var triggers []*HttpTrigger
	events := values["Events"].(map[interface{}]interface{})
	for key, value := range events {
		name := key.(string)
		props := value.(map[interface{}]interface{})
		typ := props["Type"].(string)
		if !strings.EqualFold(typ, "http") {
			continue
		}
		trigger := parseHttpTrigger(name, props["Properties"].(map[interface{}]interface{}))
		triggers = append(triggers, trigger)
	}
	return &Function{
		Name:     name,
		Triggers: triggers,
	}
}

func parseHttpTrigger(name string, props map[interface{}]interface{}) *HttpTrigger {
	authType := props["AuthType"].(string)
	values := props["Methods"].([]interface{})
	var methods []string
	for _, value := range values {
		methods = append(methods, value.(string))
	}
	return &HttpTrigger{
		Name:     name,
		AuthType: strings.ToLower(authType),
		Methods:  methods,
	}
}

type RouteConfig struct {
	Routes []*PathConfig
}

type PathConfig struct {
	Path         string
	ServiceName  string
	FunctionName string
}

type CustomDomain struct {
	Name        string
	DomainName  string
	Protocol    string
	RouteConfig *RouteConfig
}

func parseCustomDomain(name string, values map[interface{}]interface{}) *CustomDomain {
	props := values["Properties"].(map[interface{}]interface{})
	domainName := props["DomainName"].(string)
	protocol := props["Protocol"].(string)
	routeConfigMap := props["RouteConfig"].(map[interface{}]interface{})
	routesMap := routeConfigMap["Routes"].(map[interface{}]interface{})
	routeConfig := &RouteConfig{}
	for key, value := range routesMap {
		path := key.(string)
		pathConfig := parsePathConfig(path, value.(map[interface{}]interface{}))
		routeConfig.Routes = append(routeConfig.Routes, pathConfig)
	}
	return &CustomDomain{
		RouteConfig: routeConfig,
		Name:        name,
		DomainName:  domainName,
		Protocol:    protocol,
	}
}

func parsePathConfig(path string, values map[interface{}]interface{}) *PathConfig {
	serviceName := values["ServiceName"].(string)
	functionName := values["FunctionName"].(string)
	return &PathConfig{
		Path:         path,
		ServiceName:  serviceName,
		FunctionName: functionName,
	}
}
