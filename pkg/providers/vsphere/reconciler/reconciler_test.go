package reconciler_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	vspherev1 "sigs.k8s.io/cluster-api-provider-vsphere/api/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	controlplanev1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1beta1"
	addonsv1 "sigs.k8s.io/cluster-api/exp/addons/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/eks-anywhere/internal/test"
	"github.com/aws/eks-anywhere/internal/test/envtest"
	anywherev1 "github.com/aws/eks-anywhere/pkg/api/v1alpha1"
	clusterspec "github.com/aws/eks-anywhere/pkg/cluster"
	"github.com/aws/eks-anywhere/pkg/clusterapi"
	"github.com/aws/eks-anywhere/pkg/config"
	"github.com/aws/eks-anywhere/pkg/constants"
	"github.com/aws/eks-anywhere/pkg/controller"
	"github.com/aws/eks-anywhere/pkg/controller/clientutil"
	"github.com/aws/eks-anywhere/pkg/executables"
	"github.com/aws/eks-anywhere/pkg/govmomi"
	"github.com/aws/eks-anywhere/pkg/providers/vsphere"
	"github.com/aws/eks-anywhere/pkg/providers/vsphere/mocks"
	"github.com/aws/eks-anywhere/pkg/providers/vsphere/reconciler"
	vspherereconcilermocks "github.com/aws/eks-anywhere/pkg/providers/vsphere/reconciler/mocks"
	"github.com/aws/eks-anywhere/pkg/utils/ptr"
	releasev1 "github.com/aws/eks-anywhere/release/api/v1alpha1"
)

const (
	clusterNamespace = "test-namespace"
)

func TestReconcilerReconcileSuccess(t *testing.T) {
	tt := newReconcilerTest(t)
	// We want to check that the cluster status is cleaned up if validations are passed
	tt.cluster.Status.FailureMessage = ptr.String("invalid cluster")
	capiCluster := test.CAPICluster(func(c *clusterv1.Cluster) {
		c.Name = tt.cluster.Name
	})
	tt.eksaSupportObjs = append(tt.eksaSupportObjs, capiCluster)
	tt.createAllObjs()

	logger := test.NewNullLogger()
	remoteClient := env.Client()

	tt.ipValidator.EXPECT().ValidateControlPlaneIP(tt.ctx, logger, tt.buildSpec()).Return(controller.Result{}, nil)
	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	tt.govcClient.EXPECT().SearchTemplate(tt.ctx, tt.datacenterConfig.Spec.Datacenter, gomock.Any()).Return("test", nil)
	tt.govcClient.EXPECT().GetTags(tt.ctx, tt.machineConfigControlPlane.Spec.Template).Return([]string{"os:ubuntu", fmt.Sprintf("eksdRelease:%s", tt.bundle.Spec.VersionsBundles[0].EksD.Name)}, nil)
	tt.govcClient.EXPECT().ListTags(tt.ctx).Return([]executables.Tag{}, nil)

	tt.remoteClientRegistry.EXPECT().GetClient(
		tt.ctx, client.ObjectKey{Name: "workload-cluster", Namespace: "eksa-system"},
	).Return(remoteClient, nil).Times(1)
	tt.cniReconciler.EXPECT().Reconcile(tt.ctx, logger, remoteClient, tt.buildSpec())

	result, err := tt.reconciler().Reconcile(tt.ctx, logger, tt.cluster)

	tt.Expect(err).NotTo(HaveOccurred())
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.Result{}))

	tt.Expect(tt.cluster.Status.FailureMessage).To(BeNil())
}

func TestReconcilerReconcileWorkerNodesSuccess(t *testing.T) {
	tt := newReconcilerTest(t)
	tt.cluster.Name = "my-management-cluster"
	tt.cluster.SetSelfManaged()
	capiCluster := test.CAPICluster(func(c *clusterv1.Cluster) {
		c.Name = tt.cluster.Name
	})
	tt.eksaSupportObjs = append(tt.eksaSupportObjs, capiCluster)
	tt.createAllObjs()

	logger := test.NewNullLogger()

	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	tt.govcClient.EXPECT().SearchTemplate(tt.ctx, tt.datacenterConfig.Spec.Datacenter, gomock.Any()).Return("test", nil)
	tt.govcClient.EXPECT().GetTags(tt.ctx, tt.machineConfigControlPlane.Spec.Template).Return([]string{"os:ubuntu", fmt.Sprintf("eksdRelease:%s", tt.bundle.Spec.VersionsBundles[0].EksD.Name)}, nil)
	tt.govcClient.EXPECT().ListTags(tt.ctx).Return([]executables.Tag{}, nil)

	result, err := tt.reconciler().ReconcileWorkerNodes(tt.ctx, logger, tt.cluster)

	tt.Expect(err).NotTo(HaveOccurred())
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.Result{}))

	tt.ShouldEventuallyExist(tt.ctx,
		&bootstrapv1.KubeadmConfigTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-management-cluster-md-0-1",
				Namespace: constants.EksaSystemNamespace,
			},
		},
	)

	tt.ShouldEventuallyExist(tt.ctx,
		&vspherev1.VSphereMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-management-cluster-md-0-1",
				Namespace: constants.EksaSystemNamespace,
			},
		},
	)

	tt.ShouldEventuallyExist(tt.ctx,
		&clusterv1.MachineDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-management-cluster-md-0",
				Namespace: constants.EksaSystemNamespace,
			},
		},
	)
}

func TestReconcilerFailToSetUpMachineConfigCP(t *testing.T) {
	tt := newReconcilerTest(t)
	logger := test.NewNullLogger()
	tt.withFakeClient()

	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, tt.datacenterConfig, tt.machineConfigControlPlane, gomock.Any()).Return(fmt.Errorf("error"))
	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, tt.datacenterConfig, tt.machineConfigWorker, gomock.Any()).Return(nil).MaxTimes(1)
	tt.govcClient.EXPECT().SearchTemplate(tt.ctx, tt.datacenterConfig.Spec.Datacenter, tt.machineConfigControlPlane).Return("test", nil).Times(0)
	tt.govcClient.EXPECT().GetTags(tt.ctx, tt.machineConfigControlPlane.Spec.Template).Return([]string{"os:ubuntu", fmt.Sprintf("eksdRelease:%s", tt.bundle.Spec.VersionsBundles[0].EksD.Name)}, nil).Times(0)

	result, err := tt.reconciler().ValidateMachineConfigs(tt.ctx, logger, tt.buildSpec())
	tt.Expect(err).To(BeNil(), "error should be nil to prevent requeue")
	tt.Expect(result).To(Equal(controller.Result{Result: &reconcile.Result{}}), "result should stop reconciliation")
	tt.Expect(tt.cluster.Status.FailureMessage).To(HaveValue(ContainSubstring("validating vCenter setup for VSphereMachineConfig")))
}

func TestSetupEnvVars(t *testing.T) {
	tt := newReconcilerTest(t)
	tt.withFakeClient()

	err := reconciler.SetupEnvVars(context.Background(), tt.datacenterConfig, tt.client)
	tt.Expect(os.Getenv(config.EksavSphereUsernameKey)).To(Equal("user"))
	tt.Expect(os.Getenv(config.EksavSpherePasswordKey)).To(Equal("pass"))

	tt.Expect(os.Getenv(config.EksavSphereCPUsernameKey)).To(Equal("userCP"))
	tt.Expect(os.Getenv(config.EksavSphereCPPasswordKey)).To(Equal("passCP"))

	tt.Expect(err).To(BeNil())
}

func TestReconcilerControlPlaneIsNotReady(t *testing.T) {
	tt := newReconcilerTest(t)
	capiCluster := test.CAPICluster(func(c *clusterv1.Cluster) {
		c.Name = tt.cluster.Name
	})
	capiCluster.Status.Conditions = clusterv1.Conditions{
		{
			Type:               clusterapi.ControlPlaneReadyCondition,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Now()),
		},
	}
	tt.eksaSupportObjs = append(tt.eksaSupportObjs, capiCluster)
	tt.createAllObjs()

	logger := test.NewNullLogger()

	tt.ipValidator.EXPECT().ValidateControlPlaneIP(tt.ctx, logger, tt.buildSpec()).Return(controller.Result{}, nil)
	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	tt.govcClient.EXPECT().ValidateVCenterSetupMachineConfig(tt.ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	tt.govcClient.EXPECT().SearchTemplate(tt.ctx, tt.datacenterConfig.Spec.Datacenter, gomock.Any()).Return("test", nil)
	tt.govcClient.EXPECT().GetTags(tt.ctx, tt.machineConfigControlPlane.Spec.Template).Return([]string{"os:ubuntu", fmt.Sprintf("eksdRelease:%s", tt.bundle.Spec.VersionsBundles[0].EksD.Name)}, nil)
	tt.govcClient.EXPECT().ListTags(tt.ctx).Return([]executables.Tag{}, nil)

	result, err := tt.reconciler().Reconcile(tt.ctx, logger, tt.cluster)

	tt.Expect(err).NotTo(HaveOccurred())
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.ResultWithRequeue(30 * time.Second)))
}

func TestReconcilerReconcileWorkersSuccess(t *testing.T) {
	tt := newReconcilerTest(t)
	capiCluster := test.CAPICluster(func(c *clusterv1.Cluster) {
		c.Name = tt.cluster.Name
	})
	tt.eksaSupportObjs = append(tt.eksaSupportObjs, capiCluster)
	tt.createAllObjs()

	result, err := tt.reconciler().ReconcileWorkers(tt.ctx, test.NewNullLogger(), tt.buildSpec())

	tt.Expect(err).NotTo(HaveOccurred())
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.Result{}))
}

func TestReconcilerReconcileInvalidDatacenterConfig(t *testing.T) {
	tt := newReconcilerTest(t)
	logger := test.NewNullLogger()
	tt.datacenterConfig.Status.SpecValid = false
	m := "Something wrong"
	tt.datacenterConfig.Status.FailureMessage = &m
	tt.withFakeClient()

	result, err := tt.reconciler().ValidateDatacenterConfig(tt.ctx, logger, tt.buildSpec())

	tt.Expect(err).To(BeNil(), "error should be nil to prevent requeue")
	tt.Expect(result).To(Equal(controller.Result{Result: &reconcile.Result{}}), "result should stop reconciliation")
	tt.Expect(tt.cluster.Status.FailureMessage).To(HaveValue(ContainSubstring("Something wrong")))
}

func TestReconcilerDatacenterConfigNotValidated(t *testing.T) {
	tt := newReconcilerTest(t)
	logger := test.NewNullLogger()
	tt.datacenterConfig.Status.SpecValid = false
	tt.withFakeClient()

	result, err := tt.reconciler().ValidateDatacenterConfig(tt.ctx, logger, tt.buildSpec())

	tt.Expect(err).To(BeNil(), "error should be nil to prevent requeue")
	tt.Expect(result).To(Equal(controller.Result{Result: &reconcile.Result{}}), "result should stop reconciliation")
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeNil())
}

func TestReconcileCNISuccess(t *testing.T) {
	tt := newReconcilerTest(t)
	tt.withFakeClient()

	logger := test.NewNullLogger()
	remoteClient := fake.NewClientBuilder().Build()
	spec := tt.buildSpec()

	tt.remoteClientRegistry.EXPECT().GetClient(
		tt.ctx, client.ObjectKey{Name: "workload-cluster", Namespace: "eksa-system"},
	).Return(remoteClient, nil)
	tt.cniReconciler.EXPECT().Reconcile(tt.ctx, logger, remoteClient, spec)

	result, err := tt.reconciler().ReconcileCNI(tt.ctx, logger, spec)

	tt.Expect(err).NotTo(HaveOccurred())
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.Result{}))
}

func TestReconcileCNIErrorClientRegistry(t *testing.T) {
	tt := newReconcilerTest(t)
	tt.withFakeClient()

	logger := test.NewNullLogger()
	spec := tt.buildSpec()

	tt.remoteClientRegistry.EXPECT().GetClient(
		tt.ctx, client.ObjectKey{Name: "workload-cluster", Namespace: "eksa-system"},
	).Return(nil, errors.New("building client"))

	result, err := tt.reconciler().ReconcileCNI(tt.ctx, logger, spec)

	tt.Expect(err).To(MatchError(ContainSubstring("building client")))
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.Result{}))
}

func TestReconcilerReconcileControlPlaneSuccess(t *testing.T) {
	tt := newReconcilerTest(t)
	tt.createAllObjs()

	result, err := tt.reconciler().ReconcileControlPlane(tt.ctx, test.NewNullLogger(), tt.buildSpec())

	tt.Expect(err).NotTo(HaveOccurred())
	tt.Expect(tt.cluster.Status.FailureMessage).To(BeZero())
	tt.Expect(result).To(Equal(controller.Result{}))

	tt.ShouldEventuallyExist(tt.ctx,
		&addonsv1.ClusterResourceSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workload-cluster-cpi",
				Namespace: "eksa-system",
			},
		},
	)

	tt.ShouldEventuallyExist(tt.ctx,
		&controlplanev1.KubeadmControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workload-cluster",
				Namespace: "eksa-system",
			},
		},
	)

	tt.ShouldEventuallyExist(tt.ctx,
		&vspherev1.VSphereMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workload-cluster-control-plane-1",
				Namespace: "eksa-system",
			},
		},
	)

	capiCluster := test.CAPICluster(func(c *clusterv1.Cluster) {
		c.Name = "workload-cluster"
	})
	tt.ShouldEventuallyExist(tt.ctx, capiCluster)

	tt.ShouldEventuallyExist(tt.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "workload-cluster-cloud-controller-manager", Namespace: "eksa-system"}})
	tt.ShouldEventuallyExist(tt.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "workload-cluster-cloud-provider-vsphere-credentials", Namespace: "eksa-system"}})
	tt.ShouldEventuallyExist(tt.ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "workload-cluster-cpi-manifests", Namespace: "eksa-system"}})
}

func TestReconcilerReconcileControlPlaneFailure(t *testing.T) {
	tt := newReconcilerTest(t)
	tt.createAllObjs()
	spec := tt.buildSpec()
	spec.Cluster.Spec.KubernetesVersion = ""

	_, err := tt.reconciler().ReconcileControlPlane(tt.ctx, test.NewNullLogger(), spec)

	tt.Expect(err).To(HaveOccurred())
}

type reconcilerTest struct {
	t testing.TB
	*WithT
	*envtest.APIExpecter
	ctx                       context.Context
	cniReconciler             *vspherereconcilermocks.MockCNIReconciler
	govcClient                *mocks.MockProviderGovcClient
	validator                 *vsphere.Validator
	defaulter                 *vsphere.Defaulter
	remoteClientRegistry      *vspherereconcilermocks.MockRemoteClientRegistry
	cluster                   *anywherev1.Cluster
	client                    client.Client
	env                       *envtest.Environment
	bundle                    *releasev1.Bundles
	eksaSupportObjs           []client.Object
	datacenterConfig          *anywherev1.VSphereDatacenterConfig
	machineConfigControlPlane *anywherev1.VSphereMachineConfig
	machineConfigWorker       *anywherev1.VSphereMachineConfig
	ipValidator               *vspherereconcilermocks.MockIPValidator
}

func newReconcilerTest(t testing.TB) *reconcilerTest {
	ctrl := gomock.NewController(t)
	cniReconciler := vspherereconcilermocks.NewMockCNIReconciler(ctrl)
	remoteClientRegistry := vspherereconcilermocks.NewMockRemoteClientRegistry(ctrl)
	c := env.Client()

	govcClient := mocks.NewMockProviderGovcClient(ctrl)
	vcb := govmomi.NewVMOMIClientBuilder()
	validator := vsphere.NewValidator(govcClient, vcb)
	defaulter := vsphere.NewDefaulter(govcClient)
	ipValidator := vspherereconcilermocks.NewMockIPValidator(ctrl)

	bundle := test.Bundle()

	managementCluster := vsphereCluster(func(c *anywherev1.Cluster) {
		c.Name = "management-cluster"
		c.Spec.ManagementCluster = anywherev1.ManagementCluster{
			Name: c.Name,
		}
		c.Spec.BundlesRef = &anywherev1.BundlesRef{
			Name:       bundle.Name,
			Namespace:  bundle.Namespace,
			APIVersion: bundle.APIVersion,
		}
	})

	machineConfigCP := machineConfig(func(m *anywherev1.VSphereMachineConfig) {
		m.Name = "cp-machine-config"
	})
	machineConfigWN := machineConfig(func(m *anywherev1.VSphereMachineConfig) {
		m.Name = "worker-machine-config"
	})

	credentialsSecret := createSecret()
	workloadClusterDatacenter := dataCenter(func(d *anywherev1.VSphereDatacenterConfig) {
		d.Status.SpecValid = true
	})

	cluster := vsphereCluster(func(c *anywherev1.Cluster) {
		c.Name = "workload-cluster"
		c.Spec.ManagementCluster = anywherev1.ManagementCluster{
			Name: managementCluster.Name,
		}
		c.Spec.BundlesRef = &anywherev1.BundlesRef{
			Name:       bundle.Name,
			Namespace:  bundle.Namespace,
			APIVersion: bundle.APIVersion,
		}
		c.Spec.ControlPlaneConfiguration = anywherev1.ControlPlaneConfiguration{
			Count: 1,
			Endpoint: &anywherev1.Endpoint{
				Host: "1.1.1.1",
			},
			MachineGroupRef: &anywherev1.Ref{
				Kind: anywherev1.VSphereMachineConfigKind,
				Name: machineConfigCP.Name,
			},
		}
		c.Spec.DatacenterRef = anywherev1.Ref{
			Kind: anywherev1.VSphereDatacenterKind,
			Name: workloadClusterDatacenter.Name,
		}

		c.Spec.WorkerNodeGroupConfigurations = append(c.Spec.WorkerNodeGroupConfigurations,
			anywherev1.WorkerNodeGroupConfiguration{
				Count: ptr.Int(1),
				MachineGroupRef: &anywherev1.Ref{
					Kind: anywherev1.VSphereMachineConfigKind,
					Name: machineConfigWN.Name,
				},
				Name:   "md-0",
				Labels: nil,
			},
		)
	})

	tt := &reconcilerTest{
		t:                    t,
		WithT:                NewWithT(t),
		APIExpecter:          envtest.NewAPIExpecter(t, c),
		ctx:                  context.Background(),
		cniReconciler:        cniReconciler,
		govcClient:           govcClient,
		validator:            validator,
		defaulter:            defaulter,
		ipValidator:          ipValidator,
		remoteClientRegistry: remoteClientRegistry,
		client:               c,
		env:                  env,
		eksaSupportObjs: []client.Object{
			test.Namespace(clusterNamespace),
			test.Namespace(constants.EksaSystemNamespace),
			managementCluster,
			workloadClusterDatacenter,
			bundle,
			test.EksdRelease(),
			credentialsSecret,
		},
		bundle:                    bundle,
		cluster:                   cluster,
		datacenterConfig:          workloadClusterDatacenter,
		machineConfigControlPlane: machineConfigCP,
		machineConfigWorker:       machineConfigWN,
	}

	t.Cleanup(tt.cleanup)
	return tt
}

func (tt *reconcilerTest) cleanup() {
	tt.DeleteAndWait(tt.ctx, tt.allObjs()...)

	tt.DeleteAllOfAndWait(tt.ctx, &bootstrapv1.KubeadmConfigTemplate{})
	tt.DeleteAllOfAndWait(tt.ctx, &vspherev1.VSphereMachineTemplate{})
	tt.DeleteAllOfAndWait(tt.ctx, &clusterv1.MachineDeployment{})
}

func (tt *reconcilerTest) buildSpec() *clusterspec.Spec {
	tt.t.Helper()
	spec, err := clusterspec.BuildSpec(tt.ctx, clientutil.NewKubeClient(tt.client), tt.cluster)
	tt.Expect(err).NotTo(HaveOccurred())

	return spec
}

func (tt *reconcilerTest) withFakeClient() {
	tt.client = fake.NewClientBuilder().WithObjects(clientutil.ObjectsToClientObjects(tt.allObjs())...).Build()
}

func (tt *reconcilerTest) reconciler() *reconciler.Reconciler {
	return reconciler.New(tt.client, tt.validator, tt.defaulter, tt.cniReconciler, tt.remoteClientRegistry, tt.ipValidator)
}

func (tt *reconcilerTest) createAllObjs() {
	tt.t.Helper()
	envtest.CreateObjs(tt.ctx, tt.t, tt.client, tt.allObjs()...)
}

func (tt *reconcilerTest) allObjs() []client.Object {
	objs := make([]client.Object, 0, len(tt.eksaSupportObjs)+3)
	objs = append(objs, tt.eksaSupportObjs...)
	objs = append(objs, tt.cluster, tt.machineConfigControlPlane, tt.machineConfigWorker)

	return objs
}

type clusterOpt func(*anywherev1.Cluster)

func vsphereCluster(opts ...clusterOpt) *anywherev1.Cluster {
	c := &anywherev1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       anywherev1.ClusterKind,
			APIVersion: anywherev1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
		},
		Spec: anywherev1.ClusterSpec{
			KubernetesVersion: "1.22",
			ClusterNetwork: anywherev1.ClusterNetwork{
				Pods: anywherev1.Pods{
					CidrBlocks: []string{"0.0.0.0"},
				},
				Services: anywherev1.Services{
					CidrBlocks: []string{"0.0.0.0"},
				},
			},
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

type datacenterOpt func(config *anywherev1.VSphereDatacenterConfig)

func dataCenter(opts ...datacenterOpt) *anywherev1.VSphereDatacenterConfig {
	d := &anywherev1.VSphereDatacenterConfig{
		TypeMeta: metav1.TypeMeta{
			Kind:       anywherev1.VSphereDatacenterKind,
			APIVersion: anywherev1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "datacenter",
			Namespace: clusterNamespace,
		},
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

type vsphereMachineOpt func(config *anywherev1.VSphereMachineConfig)

func machineConfig(opts ...vsphereMachineOpt) *anywherev1.VSphereMachineConfig {
	m := &anywherev1.VSphereMachineConfig{
		TypeMeta: metav1.TypeMeta{
			Kind:       anywherev1.VSphereMachineConfigKind,
			APIVersion: anywherev1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
		},
		Spec: anywherev1.VSphereMachineConfigSpec{
			DiskGiB:           40,
			Datastore:         "test",
			Folder:            "test",
			NumCPUs:           2,
			MemoryMiB:         16,
			OSFamily:          "ubuntu",
			ResourcePool:      "test",
			StoragePolicyName: "test",
			Template:          "test",
			Users: []anywherev1.UserConfiguration{
				{
					Name:              "user",
					SshAuthorizedKeys: []string{"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC8ZEibIrz1AUBKDvmDiWLs9f5DnOerC4qPITiDtSOuPAsxgZbRMavBfVTxodMdAkYRYlXxK6PqNo0ve0qcOV2yvpxH1OogasMMetck6BlM/dIoo3vEY4ZoG9DuVRIf9Iry5gJKbpMDYWpx1IGZrDMOFcIM20ii2qLQQk5hfq9OqdqhToEJFixdgJt/y/zt6Koy3kix+XsnrVdAHgWAq4CZuwt1G6JUAqrpob3H8vPmL7aS+35ktf0pHBm6nYoxRhslnWMUb/7vpzWiq+fUBIm2LYqvrnm7t3fRqFx7p2sZqAm2jDNivyYXwRXkoQPR96zvGeMtuQ5BVGPpsDfVudSW21+pEXHI0GINtTbua7Ogz7wtpVywSvHraRgdFOeY9mkXPzvm2IhoqNrteck2GErwqSqb19mPz6LnHueK0u7i6WuQWJn0CUoCtyMGIrowXSviK8qgHXKrmfTWATmCkbtosnLskNdYuOw8bKxq5S4WgdQVhPps2TiMSZndjX5NTr8= ubuntu@ip-10-2-0-6"},
				},
			},
		},
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

func createSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "eksa-system",
			Name:      vsphere.CredentialsObjectName,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		Data: map[string][]byte{
			"username":    []byte("user"),
			"password":    []byte("pass"),
			"usernameCP":  []byte("userCP"),
			"passwordCP":  []byte("passCP"),
		},
	}
}
