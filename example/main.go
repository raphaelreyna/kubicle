package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/raphaelreyna/kubicle"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	ctx := context.Background()
	cluster, _ := kubicle.NewCluster(ctx, "test-cluster", 5*time.Minute)
	err := cluster.BuildAndPushImage(ctx, "my-service:latest", "./my-service")
	if err != nil {
		panic(fmt.Errorf("failed to make local available as image: %w", err))
	}
	cluster.Clientset.CoreV1().Pods("default").Create(ctx, &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "test-container",
					Image: cluster.ImageName("my-service:latest"),
				},
			},
		},
	}, metav1.CreateOptions{})

	os.WriteFile("kubeconfig.yaml", []byte(cluster.Kubeconfig), 0644)
}
