package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

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
	{
		if serviceName != "" {
			pubSrvVerInput := fc.NewPublishServiceVersionInput(serviceName)
			pubSrvVerInput.WithDescription("")
			pubSrvVerOutput, err := client.PublishServiceVersion(pubSrvVerInput)
			if err != nil {
				log.Fatalln(err)
			}
			pubAliasInput := fc.NewCreateAliasInput(serviceName)
			pubAliasInput.WithVersionID(*pubSrvVerOutput.VersionID)
			pubAliasInput.WithAliasName(releaseVersion)
			pubAliasInput.WithDescription("")
			pubAliasOutput, err := client.CreateAlias(pubAliasInput)
			if err != nil {
				log.Fatalln(err)
			}
			fmt.Println(pubAliasOutput)
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
						service := parseService(name, props)
						services = append(services, service)
					} else if typ == "Aliyun::Serverless::CustomDomain" {
						customDomain := parseCustomDomain(name, props)
						customDomains = append(customDomains, customDomain)
					}
				}
			}
			for _, service := range services {
				fmt.Printf("%s\n", service.Name)
				for _, function := range service.Functions {
					fmt.Printf("\t%s\n", function.Name)
					for _, trigger := range function.Triggers {
						fmt.Printf("\t\t%v\n", trigger)
					}
				}
			}
			for _, customDomain := range customDomains {
				fmt.Printf("%s, %s, %s\n", customDomain.Name, customDomain.DomainName, customDomain.Protocol)
				for _, route := range customDomain.RouteConfig.Routes {
					fmt.Printf("\t%v\n", route)
				}
			}
		}
	}
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
		if typ != "HTTP" {
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
		AuthType: authType,
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
	RouteConfig *RouteConfig
	Name        string
	DomainName  string
	Protocol    string
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
