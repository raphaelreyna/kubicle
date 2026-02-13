package kubicle

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"text/template"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/kind/pkg/cluster"
)

//go:embed config-template.yaml
var configTemplate string

func writeOutConfigTemplate(address string) (string, error) {
	tmplt, err := template.New("config").Parse(configTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse config template: %w", err)
	}
	data := map[string]string{
		"Address": address,
	}

	file, err := os.CreateTemp("", "kind-config-*.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	defer file.Close()

	err = tmplt.Execute(file, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute config template: %w", err)
	}

	return file.Name(), nil
}

// Cluster represents a local kind Kubernetes cluster with an associated
// container registry. It embeds a Kubernetes Clientset for direct API access.
type Cluster struct {
	Name       string
	Kubeconfig string
	Delete     func(context.Context) error
	*kubernetes.Clientset
}

// NewCluster creates or reuses a kind cluster with the given name.
// If a cluster with that name already exists, it reconnects to it.
// Otherwise, a new cluster is created with the given timeout for readiness.
// A local Docker registry is also created and attached to the cluster network.
func NewCluster(ctx context.Context, name string, timeout time.Duration) (*Cluster, error) {
	provider := cluster.NewProvider(
		cluster.ProviderWithDocker(),
	)

	clusters, err := provider.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	var kubeconfig string
	for _, c := range clusters {
		if c == name {
			kubeconfig, err = provider.KubeConfig(name, false)
			if err != nil {
				return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
			}
			break
		}
	}
	if kubeconfig == "" {
		configFilePath, err := writeOutConfigTemplate(fmt.Sprintf("%s-registry:5000", name))
		if err != nil {
			return nil, fmt.Errorf("failed to write out config template: %w", err)
		}
		defer os.Remove(configFilePath)

		err = provider.Create(name,
			cluster.CreateWithConfigFile(configFilePath),
			cluster.CreateWithWaitForReady(timeout),
			cluster.CreateWithDisplayUsage(true),
			cluster.CreateWithDisplaySalutation(true),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster: %w", err)
		}
		kubeconfig, err = provider.KubeConfig(name, false)
		if err != nil {
			return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
		}
	}

	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("failed to create client config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	err = createRegistryInNetwork(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry in network: %w", err)
	}

	cluster := Cluster{
		Name:       name,
		Kubeconfig: kubeconfig,
		Clientset:  cs,
		Delete: func(ctx context.Context) error {
			registryName := fmt.Sprintf("%s-registry", name)
			var errs []error

			if err := RemoveContainer(ctx, registryName); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove registry container: %w", err))
			}

			if err := provider.Delete(name, ""); err != nil {
				errs = append(errs, fmt.Errorf("failed to delete cluster: %w", err))
			}

			return errors.Join(errs...)
		},
	}

	return &cluster, nil
}

func createRegistryInNetwork(ctx context.Context, clusterName string) error {
	err := PullImage(ctx, "registry:2")
	if err != nil {
		return fmt.Errorf("failed to pull registry image: %w", err)
	}

	registryContainerName := fmt.Sprintf("%s-registry", clusterName)
	exists, err := ContainerExists(ctx, registryContainerName)
	if err != nil {
		return fmt.Errorf("failed to check if registry container exists: %w", err)
	}
	if exists {
		return nil
	}

	registryContainerID, err := CreateContainer(ctx, registryContainerName, "registry:2", []PortMap{
		{
			Host:      5000,
			Container: 5000,
			Protocol:  "tcp",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create registry container: %w", err)
	}

	clusterControlPlaneNodeName := fmt.Sprintf("%s-control-plane", clusterName)
	clusterNetworks, err := GetContainerNetworks(ctx, clusterControlPlaneNodeName)
	if err != nil {
		return fmt.Errorf("failed to get container networks: %w", err)
	}
	clusterNetwork := clusterNetworks[0]

	err = AttachContainerToNetwork(ctx, registryContainerID, clusterNetwork)
	if err != nil {
		return fmt.Errorf("failed to attach registry container to network: %w", err)
	}

	err = StartContainer(ctx, registryContainerID)
	if err != nil {
		return fmt.Errorf("failed to start registry container: %w", err)
	}

	return nil
}

// BuildAndPushImage builds a Docker image from localPath and pushes it to the
// cluster's local registry, making it available for use in the cluster.
func (c *Cluster) BuildAndPushImage(ctx context.Context, imageName, localPath string) error {
	return PushImageToClusterRegistry(ctx, imageName, localPath)
}

// RegistryName returns the in-cluster address of the local Docker registry.
func (c *Cluster) RegistryName() string {
	return fmt.Sprintf("%s-registry:5000", c.Name)
}

// ImageName returns the fully qualified image reference for use in Kubernetes
// pod specs, prefixed with the cluster's registry address.
func (c *Cluster) ImageName(image string) string {
	return fmt.Sprintf("%s/%s", c.RegistryName(), image)
}
