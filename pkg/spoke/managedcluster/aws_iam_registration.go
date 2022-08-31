package managedcluster

import (
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	clientset "open-cluster-management.io/api/client/cluster/clientset/versioned"
	v1 "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1"
	"open-cluster-management.io/registration/pkg/clientcert"
	"open-cluster-management.io/registration/pkg/cloudprovider/aws"
)

func NewClientForIamController(
	clusterName string,
	agentName string,
	clientCertSecretNamespace string,
	clientCertSecretName string,
	spokeSecretInformer corev1informers.SecretInformer,
	managedClusterInformer v1.ManagedClusterInformer,
	spokeKubeClient kubernetes.Interface,
	statusUpdater clientcert.StatusUpdateFunc,
	recorder events.Recorder,
	controllerName string,
	bootstrapClusterClient *clientset.Clientset,
) (factory.Controller, error) {
	return aws.NewClientCertificateController(
		clusterName,
		agentName,
		clientCertSecretNamespace,
		clientCertSecretName,
		spokeSecretInformer,
		managedClusterInformer,
		spokeKubeClient,
		statusUpdater,
		recorder,
		controllerName,
		bootstrapClusterClient,
	)
}
