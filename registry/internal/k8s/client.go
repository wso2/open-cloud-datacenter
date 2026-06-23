package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Client struct {
	cs *kubernetes.Clientset
}

// NewHarvesterClient creates a Kubernetes client for deploying Harbor into tenant namespaces.
// The provisioner runs directly on Harvester so it uses the pod's own in-cluster config.
// The provisioner's ServiceAccount (registry/registry-provisioner) has the ClusterRole
// defined in k8s/harvester-direct/rbac.yaml granting it Harbor deploy permissions.
func NewHarvesterClient() (*Client, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("build in-cluster config for harvester client: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create harvester k8s client: %w", err)
	}
	return &Client{cs: cs}, nil
}

// EnsureNamespace creates the tenant management namespace on the Harvester cluster
// if it doesn't already exist. If it does exist, it patches the labels and
// annotations to ensure they are always up to date (idempotent for both cases).
//
// Namespace name convention: "{tenantID}-management"
// This is computed by the API handler and stored in registry_deployments.namespace.
func (c *Client) EnsureNamespace(ctx context.Context, namespace, tenantID string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"lkdc.wso2.com/tenant":    tenantID,
				"lkdc.wso2.com/component": "registry",
			},
			Annotations: map[string]string{
				"lkdc.wso2.com/created-by":      "registry-provisioner",
				"lkdc.wso2.com/registry-status": "deploying",
			},
		},
	}

	_, err := c.cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err == nil {
		// Created fresh — done.
		return nil
	}
	if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", namespace, err)
	}

	// Namespace already exists (pre-created by infra or a previous failed deploy).
	// Patch labels and annotations to ensure they are always correct.
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				"lkdc.wso2.com/tenant":    tenantID,
				"lkdc.wso2.com/component": "registry",
			},
			"annotations": map[string]string{
				"lkdc.wso2.com/created-by":      "registry-provisioner",
				"lkdc.wso2.com/registry-status": "deploying",
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal namespace patch: %w", err)
	}
	if _, err := c.cs.CoreV1().Namespaces().Patch(
		ctx, namespace, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("patch namespace %s: %w", namespace, err)
	}
	return nil
}

// ApplyNetworkPolicy applies a deny-cross-tenant NetworkPolicy to the tenant's
// management namespace on Harvester, isolating Harbor from other tenants' pods.
// ingressControllerNamespace is the namespace where the ingress controller pods run
// (e.g. "kube-system" for RKE2/Harvester, "ingress-nginx" for standard installs).
func (c *Client) ApplyNetworkPolicy(ctx context.Context, namespace, ingressControllerNamespace string) error {
	protocolUDP := corev1.ProtocolUDP
	port53 := intstr.FromInt(53)
	ingressNS := ingressControllerNamespace

	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deny-cross-tenant",
			Namespace: namespace,
			Labels: map[string]string{
				"lkdc.wso2.com/managed-by": "registry-provisioner",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": ingressNS,
								},
							},
						},
						{
							PodSelector: &metav1.LabelSelector{},
						},
					},
				},
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &protocolUDP, Port: &port53},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{}},
					},
				},
				{
					// Allow external for Trivy DB updates and image pulls
					To: []networkingv1.NetworkPolicyPeer{
						{
							IPBlock: &networkingv1.IPBlock{
								CIDR: "0.0.0.0/0",
								Except: []string{
									"10.0.0.0/8",
									"172.16.0.0/12",
									"192.168.0.0/16",
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := c.cs.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = c.cs.NetworkingV1().NetworkPolicies(namespace).Update(ctx, policy, metav1.UpdateOptions{})
	}
	return err
}

// AllDeploymentsReady checks whether all Deployments in the tenant management
// namespace (on Harvester) are ready. Lists dynamically so release name changes
// (e.g. harbor-acme vs harbor-harbor) don't require code updates.
func (c *Client) AllDeploymentsReady(ctx context.Context, namespace string) (map[string]bool, error) {
	deps, err := c.cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	if len(deps.Items) == 0 {
		return map[string]bool{"waiting": false}, nil
	}
	result := make(map[string]bool, len(deps.Items))
	for _, d := range deps.Items {
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		result[d.Name] = replicas > 0 && d.Status.ReadyReplicas >= replicas
	}
	return result, nil
}

// WaitForAllReady polls until all Harbor deployments in the tenant namespace
// (on Harvester) are ready, or ctx is cancelled.
func (c *Client) WaitForAllReady(ctx context.Context, namespace string, progressCb func(map[string]bool)) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := c.AllDeploymentsReady(ctx, namespace)
			if err != nil {
				continue
			}
			if progressCb != nil {
				progressCb(status)
			}
			allReady := true
			for _, ready := range status {
				if !ready {
					allReady = false
					break
				}
			}
			if allReady {
				return nil
			}
		}
	}
}

// GetSecret reads a Kubernetes secret value from the Harvester cluster.
func (c *Client) GetSecret(ctx context.Context, namespace, name, key string) (string, error) {
	secret, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}
	return string(val), nil
}

// DeleteNamespace deletes the entire tenant management namespace from Harvester
// (called on hard delete only — removes Harbor + all its data).
func (c *Client) DeleteNamespace(ctx context.Context, namespace string) error {
	err := c.cs.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// DeletePVCs deletes all PVCs in a namespace on Harvester (hard delete path).
func (c *Client) DeletePVCs(ctx context.Context, namespace string) error {
	pvcs, err := c.cs.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pvc := range pvcs.Items {
		if err := c.cs.CoreV1().PersistentVolumeClaims(namespace).Delete(
			ctx, pvc.Name, metav1.DeleteOptions{},
		); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// ApplySecret creates or updates a Secret in the given namespace.
// Used after Harbor bootstrap to write plaintext credentials into a K8s Secret
// so dc-api can read them via its Harvester dynamic client (same pattern as dbaas operator).
func (c *Client) ApplySecret(ctx context.Context, secret *corev1.Secret) error {
	_, err := c.cs.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = c.cs.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	}
	return err
}

// DeleteSecret removes a Secret (hard delete path — called after namespace is gone,
// credentials Secret lives in registry namespace not acme-management).
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	err := c.cs.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}
