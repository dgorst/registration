package aws

import (
	"context"
	"fmt"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	clientset "open-cluster-management.io/api/client/cluster/clientset/versioned"
	v1 "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/registration/pkg/clientcert"
	"reflect"
	"time"
)

var ControllerResyncInterval = 5 * time.Minute

type controller struct {
	clusterName               string
	agentName                 string
	clientCertSecretNamespace string
	clientCertSecretName      string
	spokeSecretInformer       corev1informers.SecretInformer
	managedClusterInformer    v1.ManagedClusterInformer
	spokeKubeClient           kubernetes.Interface
	statusUpdater             clientcert.StatusUpdateFunc
	recorder                  events.Recorder
	controllerName            string
	bootstrapClusterClient    *clientset.Clientset
}

func (c *controller) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	fmt.Println("SYNCING AWS IAM CREDENTIALS for", c.clusterName)

	cluster, err := c.bootstrapClusterClient.ClusterV1().ManagedClusters().Get(ctx, c.clusterName, metav1.GetOptions{})
	if err != nil {
		fmt.Println("Err", err)
		return err
	}

	fmt.Println("Got cluster", cluster.Name)

	hubRoleArn, ok := cluster.Annotations["open-cluster-management.io/aws-iam-hub-role"]
	if !ok {
		fmt.Println("hubRoleArn not found yet")
		return nil
	}

	hubEksClusterName, ok := cluster.Annotations["open-cluster-management.io/aws-iam-hub-eks-cluster"]
	if !ok {
		fmt.Println("hubEksClusterName not found yet")
		return nil
	}

	hubRegion, ok := cluster.Annotations["open-cluster-management.io/aws-iam-hub-region"]
	if !ok {
		fmt.Println("hubRegion not found yet")
		return nil
	}

	fmt.Println("building kubeconfig")
	client, err := NewFromDefaultConfig(Opts{
		HubRoleArn:        hubRoleArn,
		HubEksClusterName: hubEksClusterName,
		HubRegion:         hubRegion,
	})
	if err != nil {
		return err
	}

	fmt.Println("building kubeconfig")
	config, err := client.BuildHubClient(ctx, c.clusterName)
	if err != nil {
		return err
	}

	kc, err := clientset.NewForConfig(config)
	if err != nil {
		return err
	}
	mc, err := kc.ClusterV1().ManagedClusters().Get(ctx, c.clusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	fmt.Println("SUCCESS - Got managed cluster using IAM credentials", mc.Name)

	c.recorder.Eventf("IAMCredentialsCreated", "IAM credentials were created", cluster.Name)

	return nil
}

func NewClientCertificateController(
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

	// Set up informer to watch the managedcluster and await the additional annotations needed to generate a kubeconfig
	managedClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			fmt.Println("UPDATE RECEIVED")
			// only need handle label update
			oldCluster, ok := oldObj.(*clusterv1.ManagedCluster)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("error to get object: %v", oldObj))
				return
			}
			newCluster, ok := newObj.(*clusterv1.ManagedCluster)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("error to get object: %v", newObj))
				return
			}
			if reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
				return
			}
		},
	})

	c := controller{
		clusterName:               clusterName,
		agentName:                 agentName,
		clientCertSecretNamespace: clientCertSecretNamespace,
		clientCertSecretName:      clientCertSecretName,
		spokeSecretInformer:       spokeSecretInformer,
		managedClusterInformer:    managedClusterInformer,
		spokeKubeClient:           spokeKubeClient,
		statusUpdater:             statusUpdater,
		recorder:                  recorder,
		controllerName:            controllerName,
		bootstrapClusterClient:    bootstrapClusterClient,
	}

	return factory.New().
		WithFilteredEventsInformersQueueKeyFunc(func(obj runtime.Object) string {
			return factory.DefaultQueueKey
		}, func(obj interface{}) bool {
			accessor, err := meta.Accessor(obj)
			if err != nil {
				return false
			}
			if accessor.GetNamespace() == c.clientCertSecretNamespace && accessor.GetName() == c.clientCertSecretName {
				return true
			}
			return false
		}, spokeSecretInformer.Informer()).
		WithSync(c.sync).
		ResyncEvery(ControllerResyncInterval).
		ToController(controllerName, recorder), nil
}
