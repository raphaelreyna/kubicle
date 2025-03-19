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
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

var _client *client.Client

func getClient() *client.Client {
	if _client == nil {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			panic(fmt.Sprintf("failed to create docker client: %v", err))
		}
		_client = cli
	}
	return _client
}

func PullImage(ctx context.Context, name string) error {
	cli := getClient()

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

func BuildImage(ctx context.Context, name string, contextTarBall io.Reader) error {
	cli := getClient()

	buildResp, err := cli.ImageBuild(ctx, contextTarBall, types.ImageBuildOptions{
		Tags:           []string{name},
		Dockerfile:     "Dockerfile",
		SuppressOutput: true,
		Remove:         true,
	})
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	return buildResp.Body.Close()
}

func GetContainerNetworks(ctx context.Context, containerName string) ([]string, error) {
	cli := getClient()

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

type PortMap struct {
	Protocol  string
	Host      int
	Container int
}

func CreateContainer(ctx context.Context, name, image string, portMappings []PortMap) (string, error) {
	containerconfig := container.Config{
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

	id, err := getClient().ContainerCreate(ctx, &containerconfig, hostConfig, nil, nil, name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return id.ID, nil
}

func StartContatiner(ctx context.Context, containerID string) error {
	cli := getClient()

	err := cli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	return nil
}

func WaitForContainerReady(ctx context.Context, timeout time.Duration, containerID string) error {
	cli := getClient()

	if timeout == 0 {
		timeout = 1 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	respChan, errChan := cli.ContainerWait(cctx, containerID, container.Healthy)
	select {
	case <-respChan:
		return nil
	case err := <-errChan:
		return fmt.Errorf("failed to wait for container: %w", err)
	}
}

func AttachContainerToNetwork(ctx context.Context, containerName string, networkName string) error {
	cli := getClient()

	err := cli.NetworkConnect(ctx, networkName, containerName, nil)
	if err != nil {
		return fmt.Errorf("failed to attach container to network: %w", err)
	}
	return nil
}

func ContainerExists(ctx context.Context, name string) (bool, error) {
	cli := getClient()

	_, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect container: %w", err)
	}
	return true, nil
}

func RemoveContainer(ctx context.Context, containerID string) error {
	cli := getClient()

	err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	return nil
}

func PushImage(ctx context.Context, name string) error {
	cli := getClient()

	credentials := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}
	credentialsJSON, err := json.Marshal(credentials)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
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

func DeleteImage(ctx context.Context, name string) error {
	cli := getClient()

	_, err := cli.ImageRemove(ctx, name, image.RemoveOptions{
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("failed to remove image: %w", err)
	}
	return nil
}

func PushImageToClusterRegistry(ctx context.Context, clusterName, imageName, contextDir string) error {
	contextTarball, err := tarDirectory(contextDir)
	if err != nil {
		return fmt.Errorf("failed to create tarball: %w", err)
	}

	err = BuildImage(ctx, fmt.Sprintf("localhost:5000/%s", imageName), contextTarball)
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}

	err = PushImage(ctx, fmt.Sprintf("localhost:5000/%s", imageName))
	if err != nil {
		return fmt.Errorf("failed to push image to cluster registry: %w", err)
	}

	err = DeleteImage(ctx, fmt.Sprintf("localhost:5000/%s", imageName))
	if err != nil {
		return fmt.Errorf("failed to delete image from local docker: %w", err)
	}

	return nil
}

func tarDirectory(dirPath string) (io.Reader, error) {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		tw := tar.NewWriter(pw)
		defer tw.Close()

		dirPathBase := filepath.Base(dirPath)

		filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
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

			// Remove the leading directory name so paths in the tar are relative
			relativePath := strings.TrimPrefix(path, dirPathBase)
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
