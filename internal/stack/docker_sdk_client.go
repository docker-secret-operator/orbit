package stack

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"go.uber.org/zap"
)

// DockerSDKClient implements DockerClient using the Docker SDK.
type DockerSDKClient struct {
	cli *client.Client
	log *zap.Logger
}

// NewDockerSDKClient creates a new Docker SDK client.
func NewDockerSDKClient(log *zap.Logger) (*DockerSDKClient, error) {
	if log == nil {
		log = zap.NewNop()
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &DockerSDKClient{
		cli: cli,
		log: log,
	}, nil
}

// CreateContainer creates a new container from options.
func (d *DockerSDKClient) CreateContainer(opts *RunOptions) (string, error) {
	if opts == nil {
		return "", fmt.Errorf("run options required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d.log.Debug("creating container",
		zap.String("service", opts.Name),
		zap.String("image", opts.Image))

	// Pull image if needed
	if err := d.PullImage(opts.Image); err != nil {
		d.log.Warn("failed to pull image, attempting with cached version",
			zap.String("image", opts.Image),
			zap.Error(err))
	}

	// Build container config
	config := &container.Config{
		Image:  opts.Image,
		Labels: opts.Labels,
		Env:    envMapToSlice(opts.Env),
	}

	// Build host config
	hostConfig := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name: opts.Restart,
		},
		AutoRemove:   opts.Remove,
		PortBindings: portMapToPortBindings(opts.Ports),
	}

	// Build network settings
	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: make(map[string]*network.EndpointSettings),
	}

	// Create container
	resp, err := d.cli.ContainerCreate(ctx, config, hostConfig, networkConfig, nil, opts.Name)
	if err != nil {
		d.log.Error("failed to create container",
			zap.String("service", opts.Name),
			zap.Error(err))
		return "", fmt.Errorf("container creation failed: %w", err)
	}

	d.log.Info("container created",
		zap.String("container_id", resp.ID),
		zap.String("service", opts.Name))

	return resp.ID, nil
}

// StartContainer starts an existing container.
func (d *DockerSDKClient) StartContainer(containerID string) error {
	if containerID == "" {
		return fmt.Errorf("container ID required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d.log.Debug("starting container",
		zap.String("container_id", containerID))

	if err := d.cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{}); err != nil {
		d.log.Error("failed to start container",
			zap.String("container_id", containerID),
			zap.Error(err))
		return fmt.Errorf("container start failed: %w", err)
	}

	d.log.Info("container started",
		zap.String("container_id", containerID))

	return nil
}

// StopContainer stops a running container.
func (d *DockerSDKClient) StopContainer(containerID string, timeout time.Duration) error {
	if containerID == "" {
		return fmt.Errorf("container ID required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()

	d.log.Debug("stopping container",
		zap.String("container_id", containerID),
		zap.Duration("timeout", timeout))

	stopTimeout := int(timeout.Seconds())
	if err := d.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
		d.log.Error("failed to stop container",
			zap.String("container_id", containerID),
			zap.Error(err))
		return fmt.Errorf("container stop failed: %w", err)
	}

	d.log.Info("container stopped",
		zap.String("container_id", containerID))

	return nil
}

// RemoveContainer removes a container.
func (d *DockerSDKClient) RemoveContainer(containerID string, force bool) error {
	if containerID == "" {
		return fmt.Errorf("container ID required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d.log.Debug("removing container",
		zap.String("container_id", containerID),
		zap.Bool("force", force))

	opts := types.ContainerRemoveOptions{Force: force}
	if err := d.cli.ContainerRemove(ctx, containerID, opts); err != nil {
		d.log.Error("failed to remove container",
			zap.String("container_id", containerID),
			zap.Error(err))
		return fmt.Errorf("container remove failed: %w", err)
	}

	d.log.Info("container removed",
		zap.String("container_id", containerID))

	return nil
}

// InspectContainer returns detailed info about a container.
func (d *DockerSDKClient) InspectContainer(containerID string) (*ContainerInfo, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		d.log.Debug("failed to inspect container",
			zap.String("container_id", containerID),
			zap.Error(err))
		return nil, fmt.Errorf("container inspect failed: %w", err)
	}

	createdTime := parseDockerTime(resp.Created)
	startedTime := parseDockerTime(resp.State.StartedAt)
	finishedTime := parseDockerTime(resp.State.FinishedAt)

	info := &ContainerInfo{
		ID:         resp.ID,
		Name:       strings.TrimPrefix(resp.Name, "/"),
		Image:      resp.Config.Image,
		Status:     ContainerStatus(resp.State.Status),
		CreatedAt:  createdTime,
		StartedAt:  startedTime,
		FinishedAt: finishedTime,
		ExitCode:   resp.State.ExitCode,
		Labels:     resp.Config.Labels,
		Ports:      portBindingsToMap(resp.NetworkSettings.Ports),
	}

	// Map health status
	if resp.State.Health != nil {
		info.Health = HealthStatus(resp.State.Health.Status)
	} else {
		info.Health = HealthUnknown
	}

	return info, nil
}

// ListContainers returns containers matching filters.
func (d *DockerSDKClient) ListContainers(filterMap map[string][]string) ([]*ContainerInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := types.ContainerListOptions{
		All: true,
	}

	if len(filterMap) > 0 {
		opts.Filters = filters.NewArgs()
		for key, values := range filterMap {
			for _, value := range values {
				opts.Filters.Add(key, value)
			}
		}
	}

	containers, err := d.cli.ContainerList(ctx, opts)
	if err != nil {
		d.log.Error("failed to list containers",
			zap.Error(err))
		return nil, fmt.Errorf("container list failed: %w", err)
	}

	result := make([]*ContainerInfo, 0, len(containers))
	for _, c := range containers {
		info, err := d.InspectContainer(c.ID)
		if err != nil {
			d.log.Warn("failed to inspect container during list",
				zap.String("container_id", c.ID))
			continue
		}
		result = append(result, info)
	}

	return result, nil
}

// GetContainerHealth returns the health status of a container.
func (d *DockerSDKClient) GetContainerHealth(containerID string) (HealthStatus, error) {
	if containerID == "" {
		return HealthUnknown, fmt.Errorf("container ID required")
	}

	info, err := d.InspectContainer(containerID)
	if err != nil {
		return HealthUnknown, err
	}

	return info.Health, nil
}

// PullImage pulls an image from registry.
func (d *DockerSDKClient) PullImage(imageName string) error {
	if imageName == "" {
		return fmt.Errorf("image name required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	d.log.Debug("pulling image",
		zap.String("image", imageName))

	reader, err := d.cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		d.log.Debug("image pull failed (may be cached)",
			zap.String("image", imageName),
			zap.Error(err))
		return err
	}
	defer reader.Close()

	// Consume the pull output to avoid resource leak. A failure here means
	// the pull stream was truncated/interrupted — the image may be
	// incomplete or stale, so surface it rather than reporting success.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("read image pull stream: %w", err)
	}

	d.log.Info("image pulled",
		zap.String("image", imageName))

	return nil
}

// GetLogs returns recent logs from a container.
func (d *DockerSDKClient) GetLogs(containerID string, lines int) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("container ID required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", lines),
	}

	reader, err := d.cli.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		d.log.Warn("failed to get logs",
			zap.String("container_id", containerID),
			zap.Error(err))
		return "", fmt.Errorf("get logs failed: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read logs failed: %w", err)
	}

	return string(data), nil
}

// WaitForContainer waits for a container to finish.
func (d *DockerSDKClient) WaitForContainer(containerID string, timeout time.Duration) (int, error) {
	if containerID == "" {
		return -1, fmt.Errorf("container ID required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()

	d.log.Debug("waiting for container",
		zap.String("container_id", containerID),
		zap.Duration("timeout", timeout))

	waitChan, errChan := d.cli.ContainerWait(ctx, containerID, container.WaitConditionNextExit)

	select {
	case result := <-waitChan:
		d.log.Debug("container exited",
			zap.String("container_id", containerID),
			zap.Int64("exit_code", result.StatusCode))
		return int(result.StatusCode), nil

	case err := <-errChan:
		d.log.Warn("error waiting for container",
			zap.String("container_id", containerID),
			zap.Error(err))
		return -1, fmt.Errorf("wait failed: %w", err)

	case <-ctx.Done():
		return -1, fmt.Errorf("wait timeout exceeded")
	}
}

// Close closes the Docker client.
func (d *DockerSDKClient) Close() error {
	return d.cli.Close()
}

// Helper functions

func parseDockerTime(dockerTime string) time.Time {
	if dockerTime == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, dockerTime)
	if err != nil {
		return time.Time{}
	}
	return t
}

func envMapToSlice(m map[string]string) []string {
	if m == nil {
		return []string{}
	}
	result := make([]string, 0, len(m))
	for k, v := range m {
		result = append(result, k+"="+v)
	}
	return result
}

func portMapToPortBindings(ports map[int]int) nat.PortMap {
	if ports == nil {
		return nat.PortMap{}
	}
	bindings := nat.PortMap{}
	for hostPort, containerPort := range ports {
		containerPortNat := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
		bindings[containerPortNat] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: fmt.Sprintf("%d", hostPort),
			},
		}
	}
	return bindings
}

func portBindingsToMap(bindings nat.PortMap) map[int]int {
	if bindings == nil {
		return make(map[int]int)
	}
	result := make(map[int]int)
	for port, binds := range bindings {
		containerPort := port.Int()
		if len(binds) > 0 {
			hostPort := binds[0].HostPort
			if hostPort != "" {
				var hp int
				fmt.Sscanf(hostPort, "%d", &hp)
				result[hp] = containerPort
			}
		}
	}
	return result
}
