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
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	clientset "open-cluster-management.io/api/client/cluster/clientset/versioned"
	clustersv1 "open-cluster-management.io/api/client/cluster/clientset/versioned/typed/cluster/v1"
	v1 "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/registration/pkg/clientcert"
	"path"
	"reflect"
	"time"
)

var (
	ControllerResyncInterval = 60 * time.Minute // TODO(@dgorst) - should be under IAM credential duration
)

const (
	kubeconfigFile = "kubeconfig"
)

type controller struct {
	clusterName            string
	agentName              string
	hubKubeconfigSecretNs  string
	hubKubeconfigSecret    string
	hubKubeconfigDir       string
	spokeSecretInformer    corev1informers.SecretInformer
	managedClusterInformer v1.ManagedClusterInformer
	spokeKubeClient        kubernetes.Interface
	statusUpdater          clientcert.StatusUpdateFunc
	recorder               events.Recorder
	controllerName         string
	hubClusterClient       *clientset.Clientset
}

func (c *controller) sync(ctx context.Context, _ factory.SyncContext) error {
	for {
		select {
		case <-time.After(15 * time.Second): // retry for errors
			if err := c.joinAndGenerateKubeconfig(ctx, c.hubClusterClient.ClusterV1().ManagedClusters()); err != nil {
				klog.Warningf("error accessing hub - will retry: %s", err)
			} else {
				c.recorder.Eventf("IAMCredentialsCreated", "IAM credentials were created", c.clusterName)
				return nil // now we are into the main resync period
			}
		case <-ctx.Done():
			klog.Infof("bootstrap completed")
			return nil
		}
	}
}

func (c *controller) joinAndGenerateKubeconfig(ctx context.Context, mci clustersv1.ManagedClusterInterface) error {
	klog.Infof("Reading managedcluster %s to obtain hub information", c.clusterName)

	// TODO(@dgorst) - we should save this data locally then we can always recover
	// Without that, if use loose access to the hub we can never generate new credentials - persist as configmap?

	cluster, err := mci.Get(ctx, c.clusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	hubRoleArn, ok := cluster.Annotations["open-cluster-management.io/aws-iam-hub-role"]
	if !ok {
		return fmt.Errorf("aws-iam-hub-role annotation not present")
	}

	hubEksClusterName, ok := cluster.Annotations["open-cluster-management.io/aws-iam-hub-eks-cluster"]
	if !ok {
		return fmt.Errorf("aws-iam-hub-eks-cluster annotation not present")
	}

	hubRegion, ok := cluster.Annotations["open-cluster-management.io/aws-iam-hub-region"]
	if !ok {
		return fmt.Errorf("aws-iam-hub-region annotation not present")
	}

	klog.Infof("managedcluster %s : hubRegion=%s hubEksClusterName=%s hubRoleArn=%s", c.clusterName, hubRegion, hubEksClusterName, hubRoleArn)
	client, err := NewFromDefaultConfig(Opts{
		HubRoleArn:        hubRoleArn,
		HubEksClusterName: hubEksClusterName,
		HubRegion:         hubRegion,
	})
	if err != nil {
		return err
	}

	klog.Infof("creating kubeconfig for hub cluster...")
	config, err := client.BuildHubClient(ctx, c.clusterName)
	if err != nil {
		return err
	}

	kc, err := clientset.NewForConfig(config)
	if err != nil {
		return err
	}

	klog.Infof("testing access to hub cluster...")
	_, err = kc.ClusterV1().ManagedClusters().Get(ctx, c.clusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	klog.Infof("testing access to hub cluster - SUCCESS")

	cfg := CreateKubeconfig(config)
	kubeconfigPath := path.Join(c.hubKubeconfigDir, kubeconfigFile)
	klog.Infof("writing kubeconfig to %s", kubeconfigPath)
	if err := clientcmd.WriteToFile(cfg, kubeconfigPath); err != nil {
		return err
	}
	return nil
}

func NewClientCertificateController(
	clusterName string,
	agentName string,
	hubKubeconfigSecretNs string,
	hubKubeconfigSecret string,
	hubKubeconfigDir string,
	spokeSecretInformer corev1informers.SecretInformer,
	managedClusterInformer v1.ManagedClusterInformer,
	spokeKubeClient kubernetes.Interface,
	statusUpdater clientcert.StatusUpdateFunc,
	recorder events.Recorder,
	controllerName string,
	hubClusterClient *clientset.Clientset,
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
		clusterName:            clusterName,
		agentName:              agentName,
		hubKubeconfigSecretNs:  hubKubeconfigSecretNs,
		hubKubeconfigSecret:    hubKubeconfigSecret,
		hubKubeconfigDir:       hubKubeconfigDir,
		spokeSecretInformer:    spokeSecretInformer,
		managedClusterInformer: managedClusterInformer,
		spokeKubeClient:        spokeKubeClient,
		statusUpdater:          statusUpdater,
		recorder:               recorder,
		controllerName:         controllerName,
		hubClusterClient:       hubClusterClient,
	}

	return factory.New().
		WithFilteredEventsInformersQueueKeyFunc(func(obj runtime.Object) string {
			return factory.DefaultQueueKey
		}, func(obj interface{}) bool {
			accessor, err := meta.Accessor(obj)
			if err != nil {
				return false
			}
			if accessor.GetNamespace() == c.hubKubeconfigSecretNs && accessor.GetName() == c.hubKubeconfigSecret {
				return true
			}
			return false
		}, spokeSecretInformer.Informer()).
		WithSync(c.sync).
		ResyncEvery(ControllerResyncInterval).
		ToController(controllerName, recorder), nil
}
