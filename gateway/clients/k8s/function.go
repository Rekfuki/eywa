package k8s

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// DeployFunction deploys function to the k8s cluster
func (c *Client) DeployFunction(request *DeployFunctionRequest, secrets []Secret) (*FunctionStatus, error) {
	envVars := []corev1.EnvVar{}
	for k, v := range request.EnvVars {
		envVars = append(envVars, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}

	labels := map[string]string{
		faasIDLabel: request.Service,
	}

	for k, v := range request.Labels {
		labels[k] = v
	}

	if request.MinReplicas == 0 {
		request.MinReplicas = defaultMinReplicas
	}

	if request.MaxReplicas == 0 {
		request.MaxReplicas = defaultMaxReplicas
	}

	if request.ScalingFactor == 0 {
		request.ScalingFactor = defaultScalingFactor
	}

	labels[faasIDLabel] = request.Service
	labels[faasMinReplicasIDLabel] = strconv.Itoa(request.MinReplicas)
	labels[faasMaxReplicasIDLabel] = strconv.Itoa(request.MaxReplicas)
	labels[faasScaleFactorIDLabel] = strconv.Itoa(request.ScalingFactor)

	replicaCount := int32(request.MinReplicas)
	resources := &apiv1.ResourceRequirements{
		Limits:   apiv1.ResourceList{},
		Requests: apiv1.ResourceList{},
	}

	// Set Memory limits
	if request.Limits != nil && len(request.Limits.Memory) > 0 {
		qty, err := resource.ParseQuantity(request.Limits.Memory)
		if err != nil {
			return nil, err
		}
		resources.Limits[apiv1.ResourceMemory] = qty
	}

	if request.Requests != nil && len(request.Requests.Memory) > 0 {
		qty, err := resource.ParseQuantity(request.Requests.Memory)
		if err != nil {
			return nil, err
		}
		resources.Requests[apiv1.ResourceMemory] = qty
	}

	// Set CPU limits
	if request.Limits != nil && len(request.Limits.CPU) > 0 {
		qty, err := resource.ParseQuantity(request.Limits.CPU)
		if err != nil {
			return nil, err
		}
		resources.Limits[apiv1.ResourceCPU] = qty
	}

	if request.Requests != nil && len(request.Requests.CPU) > 0 {
		qty, err := resource.ParseQuantity(request.Requests.CPU)
		if err != nil {
			return nil, err
		}
		resources.Requests[apiv1.ResourceCPU] = qty
	}

	imagePullPolicy := apiv1.PullIfNotPresent

	annotations := map[string]string{}
	if len(request.Annotations) > 0 {
		annotations = request.Annotations
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        request.Service,
			Annotations: annotations,
			Labels:      labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					faasIDLabel: request.Service,
				},
			},
			Replicas: &replicaCount,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: int32(0),
					},
					MaxSurge: &intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: int32(1),
					},
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        request.Service,
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Name:  request.Service,
							Image: request.Image,
							Ports: []apiv1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 8080,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env:             envVars,
							Resources:       *resources,
							ImagePullPolicy: imagePullPolicy,
						},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
					DNSPolicy:     corev1.DNSClusterFirst,
				},
			},
		},
	}

	secretVolumeProjections := []apiv1.VolumeProjection{}
	for _, secret := range secrets {
		projectedPaths := []apiv1.KeyToPath{}
		for secretKey := range secret.Data {
			projectedPaths = append(projectedPaths, apiv1.KeyToPath{Key: secretKey, Path: secretKey})
		}

		projection := &apiv1.SecretProjection{Items: projectedPaths}
		projection.Name = secret.Name
		secretProjection := apiv1.VolumeProjection{
			Secret: projection,
		}
		secretVolumeProjections = append(secretVolumeProjections, secretProjection)
	}

	if len(secretVolumeProjections) > 0 {
		volumeName := fmt.Sprintf("%s-projected-secrets", request.Service)
		projectedSecrets := apiv1.Volume{
			Name: volumeName,
			VolumeSource: apiv1.VolumeSource{
				Projected: &apiv1.ProjectedVolumeSource{
					Sources: secretVolumeProjections,
				},
			},
		}

		deployment.Spec.Template.Spec.Volumes = []corev1.Volume{projectedSecrets}

		for i := range deployment.Spec.Template.Spec.Containers {
			mount := apiv1.VolumeMount{
				Name:      volumeName,
				ReadOnly:  true,
				MountPath: faasSecretMount,
			}

			deployment.Spec.Template.Spec.Containers[i].VolumeMounts = []apiv1.VolumeMount{mount}
		}
	}

	deployment, err := c.clientset.AppsV1().
		Deployments(faasNamespace).
		Create(context.TODO(), deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        request.Service,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				faasIDLabel: request.Service,
			},
			Ports: []corev1.ServicePort{
				{
					Name:     "http",
					Protocol: corev1.ProtocolTCP,
					Port:     8080,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8080,
					},
				},
			},
		},
	}
	_, err = c.clientset.CoreV1().
		Services(faasNamespace).
		Create(context.TODO(), service, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	return deploymentToFunction(deployment)
}

// DeleteFunction deletes the function deployment and service
func (c *Client) DeleteFunction(fnName string) error {
	foregroundPolicy := metav1.DeletePropagationForeground
	opts := &metav1.DeleteOptions{PropagationPolicy: &foregroundPolicy}

	if err := c.clientset.AppsV1().
		Deployments(faasNamespace).
		Delete(context.TODO(), fnName, *opts); err != nil {
		return err
	}

	if err := c.clientset.CoreV1().
		Services(faasNamespace).
		Delete(context.TODO(), fnName, *opts); err != nil {
		return err

	}
	return nil
}

// ListFunctions returns all functions with faas id label
func (c *Client) ListFunctions() ([]FunctionStatus, error) {
	requirement, err := labels.NewRequirement(faasIDLabel, selection.Exists, []string{})
	if err != nil {
		return nil, err
	}
	return c.listFunctions(*requirement)
}

// ListFunctionsFiltered returns functions filtered by provided labels
func (c *Client) ListFunctionsFiltered(l map[string]string) ([]FunctionStatus, error) {
	requirement, err := labels.NewRequirement(faasIDLabel, selection.Exists, []string{})
	if err != nil {
		return nil, err
	}

	requirements := []labels.Requirement{*requirement}

	for k, v := range l {
		requirement, err := labels.NewRequirement(k, selection.Equals, []string{v})
		if err != nil {
			return nil, err
		}
		requirements = append(requirements, *requirement)
	}

	return c.listFunctions(requirements...)
}

// ListFunctions returns all current function deployments
func (c *Client) listFunctions(r ...labels.Requirement) ([]FunctionStatus, error) {
	selector := labels.NewSelector().Add(r...)
	deployments, err := c.deploymentLister.List(selector)
	if err != nil {
		return nil, err
	}

	fs := []FunctionStatus{}
	for _, d := range deployments {
		f, err := deploymentToFunction(d)
		if err != nil {
			return nil, err
		}
		fs = append(fs, *f)
	}

	return fs, nil
}

// GetFunctionStatus returns status of the function from k8s
func (c *Client) GetFunctionStatus(fnName string) (*FunctionStatus, error) {
	opts := metav1.GetOptions{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
	}
	deployment, err := c.clientset.AppsV1().Deployments(faasNamespace).Get(context.TODO(), fnName, opts)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	if _, found := deployment.Labels[faasIDLabel]; !found {
		return nil, nil
	}

	return deploymentToFunction(deployment)
}

func deploymentToFunction(deployment *appsv1.Deployment) (*FunctionStatus, error) {
	var replicas int32
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}

	function := &FunctionStatus{
		Name:              deployment.Name,
		Replicas:          replicas,
		Image:             deployment.Spec.Template.Spec.Containers[0].Image,
		AvailableReplicas: deployment.Status.AvailableReplicas,
		Labels:            deployment.Spec.Template.Labels,
		Annotations:       deployment.Spec.Template.Annotations,
		Namespace:         deployment.Namespace,
		Env:               map[string]string{},
	}

	for _, c := range deployment.Spec.Template.Spec.Containers {
		for _, v := range c.Env {
			function.Env[v.Name] = v.Value
		}

		for _, vm := range c.VolumeMounts {
			function.MountedSecrets = append(function.MountedSecrets, vm.Name)
		}

		function.Limits = &FunctionResources{
			CPU:    c.Resources.Limits.Cpu().String(),
			Memory: c.Resources.Limits.Memory().String(),
		}

		function.Requests = &FunctionResources{
			CPU:    c.Resources.Requests.Cpu().String(),
			Memory: c.Resources.Requests.Memory().String(),
		}
	}

	for k, v := range deployment.Spec.Template.Labels {
		if k == faasMinReplicasIDLabel {
			rc64, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil, err
			}
			function.MinReplicas = int32(rc64)
		}

		if k == faasMaxReplicasIDLabel {
			rc64, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil, err
			}
			function.MaxReplicas = int32(rc64)
		}

		if k == faasScaleFactorIDLabel {
			rc64, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil, err
			}
			function.ScalingFactor = int32(rc64)
		}
	}

	return function, nil
}

// ScaleFunction scales the function to specified replicas
func (c *Client) ScaleFunction(fnName string, replicas int32) error {
	opts := metav1.GetOptions{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
	}

	deployment, err := c.clientset.AppsV1().Deployments(faasNamespace).Get(context.TODO(), fnName, opts)
	if err != nil {
		return err
	}

	oldReplicas := *deployment.Spec.Replicas

	log.Printf("Set replicas - %s %s, %d/%d\n", deployment.Name, faasNamespace, replicas, oldReplicas)

	deployment.Spec.Replicas = &replicas

	_, err = c.clientset.AppsV1().Deployments(faasNamespace).Update(context.TODO(), deployment, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

// ScaleFromZero scales the function from zero replicas to desired
func (c *Client) ScaleFromZero(fnName string) (*FunctionZeroScaleResult, error) {
	start := time.Now()

	if val, found := c.cache.Get(fnName); found {
		cached, ok := val.(*FunctionStatus)
		if !ok {
			return &FunctionZeroScaleResult{
				Available: false,
				Found:     false,
			}, fmt.Errorf("Cache error: expected %T, received %T", &FunctionStatus{}, val)
		}
		if cached.AvailableReplicas > 0 {
			return &FunctionZeroScaleResult{
				Available: true,
				Found:     true,
			}, nil
		}
	}

	functionStatus, err := c.GetFunctionStatus(fnName)
	if err != nil {
		return &FunctionZeroScaleResult{
			Available: false,
			Found:     false,
			Duration:  time.Since(start),
		}, err
	}

	c.cache.Set(fnName, functionStatus, cache.DefaultExpiration)

	if functionStatus.AvailableReplicas == 0 {
		minReplicas := int32(1)
		if functionStatus.MinReplicas > 0 {
			minReplicas = functionStatus.MinReplicas
		}

		// TODO: move to config
		attempts := 20
		interval := time.Millisecond * 50

		err := backoff(func(attempt int) error {
			functionStatus, err := c.GetFunctionStatus(fnName)
			if err != nil {
				return err
			}

			c.cache.Set(fnName, functionStatus, cache.DefaultExpiration)

			if functionStatus.AvailableReplicas > 0 {
				return nil
			}

			err = c.ScaleFunction(fnName, minReplicas)
			if err != nil {
				return fmt.Errorf("Failed to scale function %q, err: %s", fnName, err)
			}
			return nil
		}, attempts, interval)

		if err != nil {
			return &FunctionZeroScaleResult{
				Available: false,
				Found:     true,
				Duration:  time.Since(start),
			}, err
		}

		// TODO: move to config
		maxPollCount := 1000

		for i := 0; i < maxPollCount; i++ {
			functionStatus, err := c.GetFunctionStatus(fnName)
			if err != nil {
				return &FunctionZeroScaleResult{
					Available: false,
					Found:     true,
					Duration:  time.Since(start),
				}, err
			}

			c.cache.Set(fnName, functionStatus, cache.DefaultExpiration)
			totalTime := time.Since(start)

			if functionStatus.AvailableReplicas > 0 {
				log.Printf("Function %q scaled successfully in %fs. Available replicas: %d",
					fnName, totalTime.Seconds(), functionStatus.AvailableReplicas)

				return &FunctionZeroScaleResult{
					Available: true,
					Found:     true,
					Duration:  totalTime,
				}, nil
			}

			time.Sleep(interval)
		}
	}

	return &FunctionZeroScaleResult{
		Available: true,
		Found:     true,
		Duration:  time.Since(start),
	}, nil
}

type routine func(attempt int) error

func backoff(r routine, attempts int, interval time.Duration) error {
	var err error

	for i := 0; i < attempts; i++ {
		res := r(i)
		if res != nil {
			err = res

			log.Printf("Attempt: %d, had error: %s\n", i, res)
		} else {
			err = nil
			break
		}
		time.Sleep(interval)
	}
	return err
}
