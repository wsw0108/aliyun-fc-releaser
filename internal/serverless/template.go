package serverless

import (
	"gopkg.in/yaml.v3"
)

type LogConfig struct {
	Project  string
	Logstore string
}

type VpcConfig struct {
	VpcId           string
	VSwitchIds      []string
	SecurityGroupId string
}

type HTTPTrigger struct {
	AuthType string
	Methods  []string
}

type Trigger struct {
	Name string
	Type string
	HTTP HTTPTrigger
}

type Function struct {
	Name                 string
	Handler              string
	Runtime              string
	CodeUri              string
	MemorySize           int
	InstanceConcurrency  int
	Timeout              int
	EnvironmentVariables map[string]string
	Triggers             []Trigger
}

type Service struct {
	Name           string
	Description    string
	Role           string
	LogConfig      LogConfig
	VpcConfig      VpcConfig
	InternetAccess bool
	Functions      []Function
}

type PathConfig struct {
	Path         string
	ServiceName  string
	FunctionName string
}

type RouteConfig struct {
	Routes []PathConfig
}

type CertConfig struct {
	CertName    string
	PrivateKey  string
	Certificate string
}

type CustomDomain struct {
	Name        string
	DomainName  string
	Protocol    string
	RouteConfig RouteConfig
	CertConfig  CertConfig
}

type Template struct {
	ROSTemplateFormatVersion string
	Transform                string
	Services                 []Service
	CustomDomains            []CustomDomain
}

func convertTrigger(name string, res functionEvent) (t Trigger) {
	t.Name = name
	t.Type = res.Type
	switch t.Type {
	case "HTTP":
		t.HTTP.AuthType = res.httpEvent.Properties.AuthType
		t.HTTP.Methods = res.httpEvent.Properties.Methods
	}
	return
}

func convertFunction(name string, res function) (f Function) {
	f.Name = name
	f.Handler = res.Properties.Handler
	f.Runtime = res.Properties.Runtime
	f.CodeUri = res.Properties.CodeUri
	f.MemorySize = res.Properties.MemorySize
	f.InstanceConcurrency = res.Properties.InstanceConcurrency
	f.Timeout = res.Properties.Timeout
	f.EnvironmentVariables = res.Properties.EnvironmentVariables
	for tname, trigger := range res.Events {
		t := convertTrigger(tname, trigger)
		f.Triggers = append(f.Triggers, t)
	}
	return
}

func convertService(name string, res service) (s Service) {
	s.Name = name
	s.Description = res.Properties.Description
	s.Role = res.Properties.Role
	s.LogConfig = LogConfig(res.Properties.LogConfig)
	s.VpcConfig = VpcConfig(res.Properties.VpcConfig)
	s.InternetAccess = res.Properties.InternetAccess
	for fname, function := range res.functions {
		f := convertFunction(fname, function)
		s.Functions = append(s.Functions, f)
	}
	return
}

func convertCustomDomain(name string, res domain) (d CustomDomain) {
	d.Name = name
	d.DomainName = res.Properties.DomainName
	d.Protocol = res.Properties.Protocol
	d.CertConfig.CertName = res.Properties.CertConfig.CertName
	d.CertConfig.Certificate = res.Properties.CertConfig.Certificate
	d.CertConfig.PrivateKey = res.Properties.CertConfig.PrivateKey
	for path, route := range res.Properties.RouteConfig.Routes {
		r := PathConfig{
			Path:         path,
			ServiceName:  route.ServiceName,
			FunctionName: route.FunctionName,
		}
		d.RouteConfig.Routes = append(d.RouteConfig.Routes, r)
	}
	return
}

func (t *Template) UnmarshalYAML(node *yaml.Node) error {
	var tpl template
	if err := node.Decode(&tpl); err != nil {
		return err
	}
	t.ROSTemplateFormatVersion = tpl.ROSTemplateFormatVersion
	t.Transform = tpl.Transform
	for name, res := range tpl.Resources {
		switch res.Type {
		case "Aliyun::Serverless::Service":
			s := convertService(name, res.service)
			t.Services = append(t.Services, s)
		case "Aliyun::Serverless::CustomDomain":
			d := convertCustomDomain(name, res.domain)
			t.CustomDomains = append(t.CustomDomains, d)
		}
	}
	return nil
}
