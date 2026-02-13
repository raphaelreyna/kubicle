package kubicle

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

var (
	_client     *client.Client
	_clientOnce sync.Once
	_clientErr  error
)

func getClient() (*client.Client, error) {
	_clientOnce.Do(func() {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			_clientErr = fmt.Errorf("failed to create docker client: %w", err)
			return
		}
		_client = cli
	})
	return _client, _clientErr
}

// PullImage pulls a Docker image by name from a remote registry.
func PullImage(ctx context.Context, name string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	reader, err := cli.ImagePull(ctx, name, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer reader.Close()

	// Consume the response body to ensure the request completes
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to read image pull response: %w", err)
	}
	return nil
}

// BuildImage builds a Docker image from the given tar archive build context.
func BuildImage(ctx context.Context, name string, contextTarBall io.Reader) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	buildResp, err := cli.ImageBuild(ctx, contextTarBall, types.ImageBuildOptions{
		Tags:           []string{name},
		Dockerfile:     "Dockerfile",
		SuppressOutput: true,
		Remove:         true,
	})
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	defer buildResp.Body.Close()

	// Consume the response body to ensure the build completes
	_, err = io.Copy(io.Discard, buildResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read image build response: %w", err)
	}
	return nil
}

// GetContainerNetworks returns the names of the Docker networks a container is attached to.
func GetContainerNetworks(ctx context.Context, containerName string) ([]string, error) {
	cli, err := getClient()
	if err != nil {
		return nil, err
	}

	containerJSON, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	networks := []string{}
	for network := range containerJSON.NetworkSettings.Networks {
		networks = append(networks, network)
	}
	return networks, nil
}

// PortMap describes a port mapping between a host port and a container port.
type PortMap struct {
	Protocol  string
	Host      int
	Container int
}

// CreateContainer creates a new Docker container with the given image and port mappings.
// It returns the container ID on success.
func CreateContainer(ctx context.Context, name, image string, portMappings []PortMap) (string, error) {
	cli, err := getClient()
	if err != nil {
		return "", err
	}

	containerConfig := container.Config{
		Image: image,
	}

	var hostConfig *container.HostConfig
	if len(portMappings) > 0 {
		portMap := make(nat.PortMap)
		for _, pm := range portMappings {
			portMap[nat.Port(fmt.Sprintf("%d/%s", pm.Container, pm.Protocol))] = []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", pm.Host),
				},
			}
		}
		hostConfig = &container.HostConfig{
			PortBindings: portMap,
		}
	}

	id, err := cli.ContainerCreate(ctx, &containerConfig, hostConfig, nil, nil, name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return id.ID, nil
}

// StartContainer starts a previously created Docker container.
func StartContainer(ctx context.Context, containerID string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	err = cli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	return nil
}

// WaitForContainerReady blocks until the container reports a healthy status or the timeout is reached.
// If timeout is zero, it defaults to 1 minute.
func WaitForContainerReady(ctx context.Context, timeout time.Duration, containerID string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	if timeout == 0 {
		timeout = 1 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	respChan, errChan := cli.ContainerWait(cctx, containerID, container.Healthy)
	select {
	case resp := <-respChan:
		if resp.Error != nil {
			return fmt.Errorf("container exited with error: %s", resp.Error.Message)
		}
		return nil
	case err := <-errChan:
		return fmt.Errorf("failed to wait for container: %w", err)
	}
}

// AttachContainerToNetwork connects a container to a Docker network.
func AttachContainerToNetwork(ctx context.Context, containerName string, networkName string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	err = cli.NetworkConnect(ctx, networkName, containerName, nil)
	if err != nil {
		return fmt.Errorf("failed to attach container to network: %w", err)
	}
	return nil
}

// ContainerExists reports whether a container with the given name exists.
func ContainerExists(ctx context.Context, name string) (bool, error) {
	cli, err := getClient()
	if err != nil {
		return false, err
	}

	_, err = cli.ContainerInspect(ctx, name)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect container: %w", err)
	}
	return true, nil
}

// RemoveContainer force-removes a Docker container.
func RemoveContainer(ctx context.Context, containerID string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	err = cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	return nil
}

// PushImage pushes a Docker image to its registry.
func PushImage(ctx context.Context, name string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	credentials := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}
	credentialsJSON, marshalErr := json.Marshal(credentials)
	if marshalErr != nil {
		return fmt.Errorf("failed to marshal credentials: %w", marshalErr)
	}
	credentialsJSONBase64 := base64.StdEncoding.EncodeToString(credentialsJSON)

	reader, err := cli.ImagePush(ctx, name, image.PushOptions{
		RegistryAuth: credentialsJSONBase64,
	})
	if err != nil {
		return fmt.Errorf("failed to push image: %w", err)
	}
	defer reader.Close()

	// Consume the push response to finish the request
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to read image push response: %w", err)
	}
	return nil
}

// DeleteImage force-removes a Docker image by name.
func DeleteImage(ctx context.Context, name string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	_, err = cli.ImageRemove(ctx, name, image.RemoveOptions{
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("failed to remove image: %w", err)
	}
	return nil
}

// PushImageToClusterRegistry builds a Docker image from contextDir, pushes it
// to the local cluster registry at localhost:5000, and cleans up the local copy.
func PushImageToClusterRegistry(ctx context.Context, imageName, contextDir string) error {
	contextTarball, err := tarDirectory(contextDir)
	if err != nil {
		return fmt.Errorf("failed to create tarball: %w", err)
	}

	registryImage := fmt.Sprintf("localhost:5000/%s", imageName)

	err = BuildImage(ctx, registryImage, contextTarball)
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}

	err = PushImage(ctx, registryImage)
	if err != nil {
		return fmt.Errorf("failed to push image to cluster registry: %w", err)
	}

	err = DeleteImage(ctx, registryImage)
	if err != nil {
		return fmt.Errorf("failed to delete image from local docker: %w", err)
	}

	return nil
}

func tarDirectory(dirPath string) (io.Reader, error) {
	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)

		var walkErr error
		defer func() {
			tw.Close()
			pw.CloseWithError(walkErr)
		}()

		walkErr = filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}

			fi, err := d.Info()
			if err != nil {
				return err
			}

			header, err := tar.FileInfoHeader(fi, path)
			if err != nil {
				return err
			}

			// Remove the leading directory so paths in the tar are relative
			relativePath := strings.TrimPrefix(path, dirPath)
			relativePath = strings.TrimPrefix(relativePath, string(os.PathSeparator))
			header.Name = relativePath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(tw, file)
			return err
		})
	}()

	return pr, nil
}
