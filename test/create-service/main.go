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
		configFile  string
		serviceName string
	)
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalln(err)
	}
	defaultConfigFile := filepath.Join(home, ".fcli", "config.yaml")
	flag.StringVar(&configFile, "c", defaultConfigFile, "config file of funcraft")
	flag.StringVar(&serviceName, "service-name", "", "service name")
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

	client, err := fc.NewClient(config.Endpoint, config.ApiVersion, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		log.Fatalln(err)
	}
	createServiceInput := fc.NewCreateServiceInput()
	createServiceInput.WithServiceName(serviceName)
	createServiceOutput, err := client.CreateService(createServiceInput)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(createServiceOutput)
}
