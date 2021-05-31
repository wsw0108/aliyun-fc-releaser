package serverless

import (
	"gopkg.in/yaml.v3"
)

type logConfig struct {
	Project  string `yaml:"Project"`
	Logstore string `yaml:"Logstore"`
}

type vpcConfig struct {
	VpcId           string   `yaml:"VpcId"`
	VSwitchIds      []string `yaml:"VSwitchIds"`
	SecurityGroupId string   `yaml:"SecurityGroupId"`
}

type serviceProperties struct {
	Description    string    `yaml:"Description"`
	Role           string    `yaml:"Role"`
	LogConfig      logConfig `yaml:"LogConfig"` // LogConfig: Auto
	VpcConfig      vpcConfig `yaml:"VpcConfig"`
	InternetAccess bool      `yaml:"InternetAccess"`
}

type service struct {
	Type       string            `yaml:"Type"`
	Properties serviceProperties `yaml:"Properties"`
	functions  map[string]function
}

func (s *service) UnmarshalYAML(node *yaml.Node) error {
	var params struct {
		Type       string            `yaml:"Type"`
		Properties serviceProperties `yaml:"Properties"`
	}
	if err := node.Decode(&params); err != nil {
		return err
	}
	s.Type = params.Type
	s.Properties = params.Properties
	s.functions = make(map[string]function)
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == "Type" {
			continue
		}
		if node.Content[i].Value == "Properties" {
			continue
		}
		if node.Content[i].Kind != yaml.ScalarNode || node.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		var fun function
		if err := node.Content[i+1].Decode(&fun); err != nil {
			return err
		}
		if fun.Type == "Aliyun::Serverless::Function" {
			s.functions[node.Content[i].Value] = fun
		}
	}
	return nil
}

type functionProperties struct {
	Handler              string            `yaml:"Handler"`
	Runtime              string            `yaml:"Runtime"`
	CodeUri              string            `yaml:"CodeUri"`
	MemorySize           int               `yaml:"MemorySize"`
	InstanceConcurrency  int               `yaml:"InstanceConcurrency"`
	Timeout              int               `yaml:"Timeout"`
	EnvironmentVariables map[string]string `yaml:"EnvironmentVariables"`
}

type httpEventProperties struct {
	AuthType string   `yaml:"AuthType"`
	Methods  []string `yaml:"Methods"`
}

type httpEvent struct {
	Properties httpEventProperties `yaml:"Properties"`
}

type functionEvent struct {
	Type      string `yaml:"Type"`
	httpEvent httpEvent
}

func (e *functionEvent) UnmarshalYAML(node *yaml.Node) error {
	var params struct {
		Type string `yaml:"Type"`
	}
	if err := node.Decode(&params); err != nil {
		return err
	}
	e.Type = params.Type
	if e.Type == "HTTP" {
		if err := node.Decode(&e.httpEvent); err != nil {
			return err
		}
	}
	return nil
}

type function struct {
	Type       string                   `yaml:"Type"`
	Properties functionProperties       `yaml:"Properties"`
	Events     map[string]functionEvent `yaml:"Events"`
}

type pathConfig struct {
	ServiceName  string `yaml:"ServiceName"`
	FunctionName string `yaml:"FunctionName"`
}

type routeConfig struct {
	Routes map[string]pathConfig `yaml:"Routes"`
}

type domainProperties struct {
	DomainName  string      `yaml:"DomainName"`
	Protocol    string      `yaml:"Protocol"`
	RouteConfig routeConfig `yaml:"RouteConfig"`
}

type domain struct {
	Type       string           `yaml:"Type"`
	Properties domainProperties `yaml:"Properties"`
}

type resource struct {
	Type    string
	service service
	domain  domain
}

func (res *resource) UnmarshalYAML(node *yaml.Node) error {
	var params struct {
		Type string `yaml:"Type"`
	}
	if err := node.Decode(&params); err != nil {
		return err
	}
	res.Type = params.Type
	if res.Type == "Aliyun::Serverless::Service" {
		if err := node.Decode(&res.service); err != nil {
			return err
		}
	} else if res.Type == "Aliyun::Serverless::CustomDomain" {
		if err := node.Decode(&res.domain); err != nil {
			return err
		}
	}
	return nil
}

type template struct {
	ROSTemplateFormatVersion string              `yaml:"ROSTemplateFormatVersion"`
	Transform                string              `yaml:"Transform"`
	Resources                map[string]resource `yaml:"Resources"`
}
