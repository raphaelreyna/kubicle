# Kubicle

Kubicle is a Go library for running local kubernetes clusters using kind along with a local registry.
Kubicle aims to acts as a single-point-of-entry for creating clusters, accessing the clusters API and making local code available via the local registry.


## Example

Get your code running in a kubernetes cluster.

```go
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
    // creates a new cluster if "test-cluster" isnt found
	cluster, _ := kubicle.NewCluster(ctx, "test-cluster", 5*time.Minute)
    // build the local service image and push it to the clusters registry
	cluster.BuildAndPushImage(ctx, "my-service:latest", "./my-service")
    // the kubernetes api clientset is readily available.
	cluster.Clientset.CoreV1().Pods("default").Create(ctx, &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "test-container",
                    // our service image is available in the 
                    // clusters local registry
					Image: cluster.ImageName("my-service:latest"),
				},
			},
		},
	}, metav1.CreateOptions{})

    // kubicle.Cluster holds the kubeconfig yaml
	os.WriteFile("kubeconfig.yaml", []byte(cluster.Kubeconfig), 0644)
}
```