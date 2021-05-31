package main

import (
	"fmt"
	"log"
	"os"

	"github.com/wsw0108/aliyun-fc-releaser/internal/serverless"
	"gopkg.in/yaml.v3"
)

func main() {
	configFile := os.Args[1]
	content, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(string(content))
	var tpl serverless.Template
	err = yaml.Unmarshal(content, &tpl)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(tpl)
}
