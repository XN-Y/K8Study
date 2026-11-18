package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func main() {
	namespace := flag.String("namespace", "go-lab", "namespace to query")
	flag.Parse()
	if *namespace == "" {
		log.Fatal("namespace must be specified")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("load service account configuration: %v", err)
	}
	client, err := corev1.NewForConfig(config)
	if err != nil {
		log.Fatalf("create Kubernetes client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pods, err := client.Pods(*namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=shipping",
	})
	if err != nil {
		log.Fatalf("list pods in %s: %v", *namespace, err)
	}

	for _, pod := range pods.Items {
		fmt.Printf("%s/%s\t%s\n", pod.Namespace, pod.Name, pod.Status.Phase)
	}
	fmt.Printf("Found %d shipping pods in %s\n", len(pods.Items), *namespace)
}
