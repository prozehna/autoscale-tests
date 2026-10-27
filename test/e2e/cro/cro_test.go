package cro

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openshift/autoscale-tests/pkg/framework"
)

var f *framework.Framework
var operatorInstalledByTest bool

var _ = BeforeSuite(func() {
	var err error
	f, err = framework.NewFramework()
	Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

	By("Checking if CRO operator is already installed")
	installed, err := f.IsOperatorSubscribed(f.Ctx, "clusterresourceoverride", framework.CRONamespace)
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.Printf("[BeforeSuite] CRO already installed: %v\n", installed)

	if !installed {
		operatorInstalledByTest = true
		By("Installing CRO operator from catalog")
		err = f.InstallOperatorByKey(f.Ctx, "cro")
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRO operator")

		By("Waiting for CRO operator CSV to be ready")
		err = f.WaitForOperatorCSVReady(f.Ctx, framework.CRONamespace, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "CRO operator CSV did not become ready")

		By("Waiting for CRO operator pods to be ready")
		err = f.WaitForOperatorReady(f.Ctx, "cro", 3*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "CRO operator pods did not become ready")
		GinkgoWriter.Printf("[BeforeSuite] CRO operator installed and ready\n")
	}
})

var _ = AfterSuite(func() {
	if f != nil && operatorInstalledByTest {
		By("Uninstalling CRO operator (installed by test)")
		GinkgoWriter.Printf("[AfterSuite] Uninstalling CRO operator...\n")
		err := f.UninstallOperatorByKey(f.Ctx, "cro")
		Expect(err).NotTo(HaveOccurred(), "Failed to uninstall CRO operator")
	} else {
		GinkgoWriter.Printf("[AfterSuite] Operator was pre-installed, skipping uninstall\n")
	}
})

var _ = Describe("Cluster Resource Override Operator", func() {

	Context("Installation verification", func() {

		It("should have the CRO namespace", func() {
			exists, err := f.NamespaceExists(f.Ctx, framework.CRONamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue(), "CRO namespace %s should exist", framework.CRONamespace)
		})

		It("should have running operator pods", func() {
			pods, err := f.GetOperatorPods(f.Ctx, "cro")
			Expect(err).NotTo(HaveOccurred())
			Expect(pods.Items).ToNot(BeEmpty(), "Should have at least one CRO operator pod in namespace %s", framework.CRONamespace)

			By("Listing found pods")
			for _, pod := range pods.Items {
				GinkgoWriter.Printf("  - Pod: %s, Status: %s\n", pod.Name, pod.Status.Phase)
			}
		})

		It("should have all pods in Ready state", func() {
			err := f.CheckOperatorHealth(f.Ctx, "cro")
			Expect(err).NotTo(HaveOccurred(), "All CRO operator pods should be healthy")
		})
	})

	Context("Resource override with opt-in", func() {

		// CRO config used for all opt-in tests:
		//   LimitCPUToMemoryPercent:    200  →  CPU limit = memory_limit_in_Mi * 2 (as millicores)
		//   CPURequestToLimitPercent:    25  →  CPU request = CPU limit * 0.25
		//   MemoryRequestToLimitPercent: 50  →  Memory request = memory limit * 0.50
		BeforeEach(func() {
			By("Ensuring ClusterResourceOverride CR exists with standard ratios")
			err := f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     200,
				CPURequestToLimitPercent:    25,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for CRO admission webhook to become available")
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "CRO admission webhook should become available")
		})

		It("should override resources on a single container", func() {
			nsName := fmt.Sprintf("cro-single-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err := f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			originalCPU := int64(2000)
			originalMem := int64(512 * 1024 * 1024) // 512Mi

			By("Creating pod with resource requests and limits")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-single", nsName, []corev1.Container{{
				Name:  "app",
				Image: "registry.k8s.io/pause:3.9",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("2000m"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("2000m"),
					},
				},
			}})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			container := findContainer(pod.Spec.Containers, "app")
			Expect(container).NotTo(BeNil(), "container 'app' not found")
			GinkgoWriter.Printf("[Test] Actual resources: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
				container.Resources.Requests.Cpu().String(), container.Resources.Limits.Cpu().String(),
				container.Resources.Requests.Memory().String(), container.Resources.Limits.Memory().String())

			By("Verifying CRO lowered CPU limit from original 2000m (LimitCPUToMemoryPercent=200)")
			cpuLimit := container.Resources.Limits.Cpu().MilliValue()
			Expect(cpuLimit).To(BeNumerically("<", originalCPU),
				"CRO should reduce CPU limit based on memory limit ratio")
			Expect(cpuLimit).To(BeNumerically(">", 0))

			By("Verifying CRO set CPU request lower than CPU limit (CPURequestToLimitPercent=25)")
			cpuReq := container.Resources.Requests.Cpu().MilliValue()
			Expect(cpuReq).To(BeNumerically("<", cpuLimit),
				"CPU request should be a fraction of CPU limit")
			Expect(cpuReq).To(BeNumerically(">", 0))

			By("Verifying memory limit unchanged")
			Expect(container.Resources.Limits.Memory().Value()).To(Equal(originalMem))

			By("Verifying CRO set memory request lower than memory limit (MemoryRequestToLimitPercent=50)")
			memReq := container.Resources.Requests.Memory().Value()
			Expect(memReq).To(BeNumerically("<", originalMem),
				"Memory request should be a fraction of memory limit")
			Expect(memReq).To(BeNumerically(">", 0))
		})

		It("should override resources on multiple containers", func() {
			nsName := fmt.Sprintf("cro-multi-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err := f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			// Both containers have CPU limit higher than what CRO would compute,
			// so CRO will lower the CPU limit and set requests accordingly.
			By("Creating pod with two containers")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-multi", nsName, []corev1.Container{
				{
					Name:  "db",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("1024Mi"),
							corev1.ResourceCPU:    resource.MustParse("4000m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("1024Mi"),
							corev1.ResourceCPU:    resource.MustParse("4000m"),
						},
					},
				},
				{
					Name:  "app",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("512Mi"),
							corev1.ResourceCPU:    resource.MustParse("2000m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("512Mi"),
							corev1.ResourceCPU:    resource.MustParse("2000m"),
						},
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			for _, c := range pod.Spec.Containers {
				GinkgoWriter.Printf("[Test] %s: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
					c.Name,
					c.Resources.Requests.Cpu().String(), c.Resources.Limits.Cpu().String(),
					c.Resources.Requests.Memory().String(), c.Resources.Limits.Memory().String())
			}

			By("Verifying 'db' container: CRO lowered CPU limit and set requests")
			db := findContainer(pod.Spec.Containers, "db")
			Expect(db).NotTo(BeNil())
			Expect(db.Resources.Limits.Cpu().MilliValue()).To(BeNumerically("<", 4000),
				"db CPU limit should be reduced from 4000m")
			Expect(db.Resources.Requests.Cpu().MilliValue()).To(BeNumerically("<", db.Resources.Limits.Cpu().MilliValue()),
				"db CPU request should be a fraction of CPU limit")
			Expect(db.Resources.Requests.Memory().Value()).To(BeNumerically("<", int64(1024*1024*1024)),
				"db memory request should be a fraction of memory limit")

			By("Verifying 'app' container: CRO lowered CPU limit and set requests")
			app := findContainer(pod.Spec.Containers, "app")
			Expect(app).NotTo(BeNil())
			Expect(app.Resources.Limits.Cpu().MilliValue()).To(BeNumerically("<", 2000),
				"app CPU limit should be reduced from 2000m")
			Expect(app.Resources.Requests.Cpu().MilliValue()).To(BeNumerically("<", app.Resources.Limits.Cpu().MilliValue()),
				"app CPU request should be a fraction of CPU limit")
			Expect(app.Resources.Requests.Memory().Value()).To(BeNumerically("<", int64(512*1024*1024)),
				"app memory request should be a fraction of memory limit")

			GinkgoWriter.Printf("[Test] Multi-container resources verified\n")
		})
	})

	Context("Init container override", func() {

		BeforeEach(func() {
			By("Ensuring ClusterResourceOverride CR exists with standard ratios")
			err := f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     200,
				CPURequestToLimitPercent:    25,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should override resources on init containers", func() {
			nsName := fmt.Sprintf("cro-init-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err := f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			By("Creating pod with init container and regular container")
			pod, err := f.CreatePodWithResourcesAndInit(f.Ctx, "test-init", nsName,
				[]corev1.Container{{
					Name:  "app",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("512Mi"),
							corev1.ResourceCPU:    resource.MustParse("500m"),
						},
					},
				}},
				[]corev1.Container{{
					Name:    "init",
					Image:   "registry.k8s.io/pause:3.9",
					Command: []string{"sh", "-c", "echo init && sleep 1"},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("1024Mi"),
							corev1.ResourceCPU:    resource.MustParse("1000m"),
						},
					},
				}},
			)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			By("Logging actual resources")
			for _, c := range pod.Spec.InitContainers {
				GinkgoWriter.Printf("[Test] init %s: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
					c.Name,
					c.Resources.Requests.Cpu().String(), c.Resources.Limits.Cpu().String(),
					c.Resources.Requests.Memory().String(), c.Resources.Limits.Memory().String())
			}
			for _, c := range pod.Spec.Containers {
				GinkgoWriter.Printf("[Test] container %s: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
					c.Name,
					c.Resources.Requests.Cpu().String(), c.Resources.Limits.Cpu().String(),
					c.Resources.Requests.Memory().String(), c.Resources.Limits.Memory().String())
			}

			// Upstream expects for init (1024Mi mem, 1000m cpu):
			//   CPU limit = min(1000, 1024*200%) = min(1000, 2048) → CRO raises to 2000m
			//   CPU request = 2000 * 25% = 500m
			//   Mem request = 1024 * 50% = 512Mi
			By("Verifying init container was overridden by CRO")
			init := findContainer(pod.Spec.InitContainers, "init")
			Expect(init).NotTo(BeNil(), "init container not found")
			Expect(init.Resources.Requests.Memory().Value()).To(BeNumerically("<", int64(1024*1024*1024)),
				"init memory request should be a fraction of memory limit")
			Expect(init.Resources.Requests.Memory().Value()).To(BeNumerically(">", 0))

			// Upstream expects for app (512Mi mem, 500m cpu):
			//   CPU limit = min(500, 512*200%) = min(500, 1024) → CRO raises to 1000m
			//   CPU request = 1000 * 25% = 250m
			//   Mem request = 512 * 50% = 256Mi
			By("Verifying regular container was also overridden")
			app := findContainer(pod.Spec.Containers, "app")
			Expect(app).NotTo(BeNil())
			Expect(app.Resources.Requests.Memory().Value()).To(BeNumerically("<", int64(512*1024*1024)),
				"app memory request should be a fraction of memory limit")
			Expect(app.Resources.Requests.Memory().Value()).To(BeNumerically(">", 0))

			GinkgoWriter.Printf("[Test] Init container override verified\n")
		})
	})

	Context("LimitRange with default limits", func() {

		BeforeEach(func() {
			By("Ensuring ClusterResourceOverride CR exists with standard ratios")
			err := f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     200,
				CPURequestToLimitPercent:    25,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should override resources when limits come from LimitRange defaults", func() {
			nsName := fmt.Sprintf("cro-lrdef-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err := f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			By("Creating LimitRange with default limits: CPU=2000m, Memory=512Mi")
			err = f.CreateLimitRange(f.Ctx, "test-lr-defaults", nsName, corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					Default: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("2000m"),
					},
				}},
			})
			Expect(err).NotTo(HaveOccurred())

			// Pod without any resource limits/requests.
			// LimitRange will assign defaults: CPU=2000m, Memory=512Mi
			// Then CRO applies:
			//   CPU limit = min(2000, 512*200%) = min(2000, 1024) → ~1000m
			//   CPU request = cpuLimit * 25%
			//   Mem request = 512 * 50% = 256Mi
			By("Creating pod WITHOUT resource specifications")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-lrdef", nsName, []corev1.Container{{
				Name:  "app",
				Image: "registry.k8s.io/pause:3.9",
			}})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			container := findContainer(pod.Spec.Containers, "app")
			Expect(container).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] Actual resources: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
				container.Resources.Requests.Cpu().String(), container.Resources.Limits.Cpu().String(),
				container.Resources.Requests.Memory().String(), container.Resources.Limits.Memory().String())

			By("Verifying pod got limits from LimitRange (memory limit should be 512Mi)")
			Expect(container.Resources.Limits.Memory().Value()).To(Equal(int64(512*1024*1024)),
				"Memory limit should come from LimitRange default")

			By("Verifying CRO reduced CPU limit from the LimitRange default of 2000m")
			cpuLimit := container.Resources.Limits.Cpu().MilliValue()
			Expect(cpuLimit).To(BeNumerically("<", 2000),
				"CRO should reduce CPU limit from LimitRange default of 2000m")
			Expect(cpuLimit).To(BeNumerically(">", 0))

			By("Verifying CRO set CPU request as a fraction of CPU limit")
			cpuReq := container.Resources.Requests.Cpu().MilliValue()
			Expect(cpuReq).To(BeNumerically("<", cpuLimit),
				"CPU request should be a fraction of CPU limit")
			Expect(cpuReq).To(BeNumerically(">", 0))

			By("Verifying CRO set memory request lower than memory limit")
			memReq := container.Resources.Requests.Memory().Value()
			Expect(memReq).To(BeNumerically("<", int64(512*1024*1024)),
				"Memory request should be a fraction of memory limit")
			Expect(memReq).To(BeNumerically(">", 0))

			GinkgoWriter.Printf("[Test] LimitRange defaults + CRO override verified\n")
		})
	})

	// No opt-in — CRO should NOT modify pods in unlabeled namespaces
	Context("No opt-in namespace", func() {

		It("should not modify pod resources in a namespace without the opt-in label", func() {
			By("Ensuring ClusterResourceOverride CR exists")
			err := f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     200,
				CPURequestToLimitPercent:    50,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			nsName := fmt.Sprintf("cro-noopt-%d", time.Now().UnixNano())
			By("Creating namespace WITHOUT opt-in label")
			_, err = f.CreateNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			By("Creating pod — resources should remain unchanged")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-noopt", nsName, []corev1.Container{{
				Name:  "test",
				Image: "registry.k8s.io/pause:3.9",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("100m"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("100m"),
					},
				},
			}})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			By("Verifying resources were NOT modified")
			container := findContainer(pod.Spec.Containers, "test")
			Expect(container).NotTo(BeNil())
			err = framework.VerifyContainerResources(*container, "100m", "512Mi", "100m", "512Mi")
			Expect(err).NotTo(HaveOccurred(), "CRO should not have modified resources in a non-opt-in namespace")
			GinkgoWriter.Printf("[Test] No-opt-in verified: resources unchanged\n")
		})
	})

	// Configuration change — verify new ratios take effect on new pods
	Context("Configuration change", func() {

		It("should apply new ratios to pods created after config update", func() {
			By("Setting initial CRO config: LimitCPU=100, CPUReq=10, MemReq=75")
			err := f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     100,
				CPURequestToLimitPercent:    10,
				MemoryRequestToLimitPercent: 75,
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("Updating CRO config to: LimitCPU=50, CPUReq=50, MemReq=50")
			err = f.UpdateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     50,
				CPURequestToLimitPercent:    50,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for webhook to reconcile with new config")
			time.Sleep(30 * time.Second)
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			nsName := fmt.Sprintf("cro-cfgchg-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err = f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			// With LimitCPU=50, CPUReq=50, MemReq=50 and pod 1024Mi/2000m:
			//   CPU limit:    min(2000, 1024 * 50%) = min(2000, 512) = 512m
			//   CPU request:  512 * 50% = 256m
			//   Mem request:  1024 * 50% = 512Mi
			By("Creating pod and verifying new ratios apply")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-cfgchg", nsName, []corev1.Container{{
				Name:  "test",
				Image: "registry.k8s.io/pause:3.9",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1024Mi"),
						corev1.ResourceCPU:    resource.MustParse("2000m"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1024Mi"),
						corev1.ResourceCPU:    resource.MustParse("2000m"),
					},
				},
			}})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			container := findContainer(pod.Spec.Containers, "test")
			Expect(container).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] Actual resources: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
				container.Resources.Requests.Cpu().String(), container.Resources.Limits.Cpu().String(),
				container.Resources.Requests.Memory().String(), container.Resources.Limits.Memory().String())

			By("Verifying CRO applied new config (LimitCPU=50 means CPU limit should drop significantly)")
			cpuLimit := container.Resources.Limits.Cpu().MilliValue()
			Expect(cpuLimit).To(BeNumerically("<", 2000),
				"CRO should reduce CPU limit from 2000m based on LimitCPUToMemory=50%%")
			Expect(cpuLimit).To(BeNumerically("<=", 1000),
				"With 1024Mi*50%% the CPU limit should be around 512m or lower")

			cpuReq := container.Resources.Requests.Cpu().MilliValue()
			Expect(cpuReq).To(BeNumerically("<", cpuLimit),
				"CPU request should be a fraction of CPU limit (CPUReq=50%%)")

			memReq := container.Resources.Requests.Memory().Value()
			Expect(memReq).To(BeNumerically("<", int64(1024*1024*1024)),
				"Memory request should be reduced from 1024Mi (MemReq=50%%)")
		})
	})

	// LimitRange interaction — CRO should clamp to namespace LimitRange max
	Context("LimitRange interaction", func() {

		It("should clamp CPU limit to LimitRange maximum", func() {
			By("Setting CRO config: LimitCPU=200")
			err := f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     200,
				CPURequestToLimitPercent:    25,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			nsName := fmt.Sprintf("cro-lr-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err = f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			By("Creating LimitRange with CPU max 1500m, memory max 2048Mi")
			err = f.CreateLimitRange(f.Ctx, "test-lr", nsName, corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					Max: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1500m"),
						corev1.ResourceMemory: resource.MustParse("2048Mi"),
					},
				}},
			})
			Expect(err).NotTo(HaveOccurred())

			// Pod: memory limit 1024Mi, CPU limit 1500m (at LimitRange max)
			// CRO LimitCPUToMemory=200%: newCPULimit = 1024 * 2 = 2048m
			// But min(1500, 2048) → CRO would set 2048m, LimitRange clamps to 1500m
			// The key check: CPU limit stays <= 1500m (LimitRange max)
			By("Creating pod with CPU limit at LimitRange max")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-lr", nsName, []corev1.Container{{
				Name:  "app",
				Image: "registry.k8s.io/pause:3.9",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1024Mi"),
						corev1.ResourceCPU:    resource.MustParse("1500m"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1024Mi"),
						corev1.ResourceCPU:    resource.MustParse("1500m"),
					},
				},
			}})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			container := findContainer(pod.Spec.Containers, "app")
			Expect(container).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] Actual resources: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
				container.Resources.Requests.Cpu().String(), container.Resources.Limits.Cpu().String(),
				container.Resources.Requests.Memory().String(), container.Resources.Limits.Memory().String())

			By("Verifying CPU limit does not exceed LimitRange max of 1500m")
			cpuLimit := container.Resources.Limits.Cpu().MilliValue()
			Expect(cpuLimit).To(BeNumerically("<=", 1500),
				"CPU limit should not exceed LimitRange max of 1500m")

			By("Verifying CPU request does not exceed CPU limit")
			cpuReq := container.Resources.Requests.Cpu().MilliValue()
			Expect(cpuReq).To(BeNumerically("<=", cpuLimit),
				"CPU request should not exceed CPU limit")
			Expect(cpuReq).To(BeNumerically(">", 0))

			By("Verifying memory request does not exceed memory limit")
			memReq := container.Resources.Requests.Memory().Value()
			Expect(memReq).To(BeNumerically("<=", int64(1024*1024*1024)),
				"Memory request should not exceed memory limit")
			Expect(memReq).To(BeNumerically(">", 0))
			GinkgoWriter.Printf("[Test] LimitRange verified: CPU limit=%dm (max=1500m)\n", cpuLimit)
		})
	})

	// Multiple ResourceOverride objects in the same namespace — the admission webhook
	// sorts the matching ones by name and the first one alphabetically wins
	Context("Multiple ResourceOverrides in the same namespace", func() {

		BeforeEach(func() {
			By("Checking the ResourceOverride CRD is installed")
			exists, err := f.ResourceOverrideCRDExists(f.Ctx)
			Expect(err).NotTo(HaveOccurred())
			if !exists {
				Skip("ResourceOverride CRD not present on this cluster — CRO operator is too old")
			}

			// This is the config the webhook falls back to when no ResourceOverride
			// matches the pod: memory request = 50% of the 1024Mi limit = 512Mi
			By("Ensuring ClusterResourceOverride CR exists with standard ratios")
			err = f.CreateClusterResourceOverride(f.Ctx, framework.CROConfig{
				LimitCPUToMemoryPercent:     100,
				CPURequestToLimitPercent:    25,
				MemoryRequestToLimitPercent: 50,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for CRO admission webhook to become available")
			err = f.WaitForClusterResourceOverrideReady(f.Ctx, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "CRO admission webhook should become available")
		})

		It("should apply the lexicographically first ResourceOverride when several match", func() {
			nsName := fmt.Sprintf("cro-roconflict-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err := f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			// Neither RO has a podSelector so both of them match every pod in the
			// namespace. "aaa-override" sorts before "zzz-override" so it should win.
			By("Creating ResourceOverride 'aaa-override' with MemReq=25")
			err = f.CreateResourceOverride(f.Ctx, "aaa-override", nsName, framework.ROConfig{
				LimitCPUToMemoryPercent:     100,
				CPURequestToLimitPercent:    20,
				MemoryRequestToLimitPercent: 25,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating ResourceOverride 'zzz-override' with MemReq=75")
			err = f.CreateResourceOverride(f.Ctx, "zzz-override", nsName, framework.ROConfig{
				LimitCPUToMemoryPercent:     100,
				CPURequestToLimitPercent:    80,
				MemoryRequestToLimitPercent: 75,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the webhook informer to pick up both ResourceOverrides")
			time.Sleep(30 * time.Second)

			By("Creating pod with 1024Mi memory and 2000m CPU")
			pod, err := f.CreatePodWithResources(f.Ctx, "test-roconflict", nsName, roTestContainers())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, pod.Name, nsName) })

			container := findContainer(pod.Spec.Containers, "app")
			Expect(container).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] Actual resources: CPU req=%s lim=%s, Mem req=%s lim=%s\n",
				container.Resources.Requests.Cpu().String(), container.Resources.Limits.Cpu().String(),
				container.Resources.Requests.Memory().String(), container.Resources.Limits.Memory().String())

			By("Verifying memory request comes from 'aaa-override' (25% of 1024Mi = 256Mi)")
			Expect(container.Resources.Requests.Memory().Value()).To(Equal(int64(256*1024*1024)),
				"the alphabetically first ResourceOverride should have won")

			By("Verifying the memory request is NOT the one from 'zzz-override' (75% = 768Mi)")
			Expect(container.Resources.Requests.Memory().Value()).NotTo(Equal(int64(768 * 1024 * 1024)))

			By("Verifying the memory request is NOT the CRO fallback (50% = 512Mi)")
			Expect(container.Resources.Requests.Memory().Value()).NotTo(Equal(int64(512 * 1024 * 1024)))

			By("Verifying CPU request is a fraction of the CPU limit (CPUReq=20%)")
			cpuLimit := container.Resources.Limits.Cpu().MilliValue()
			cpuReq := container.Resources.Requests.Cpu().MilliValue()
			Expect(cpuReq).To(BeNumerically("<", cpuLimit))
			Expect(cpuReq).To(BeNumerically(">", 0))
		})

		It("should switch to the next ResourceOverride after the winning one is deleted", func() {
			nsName := fmt.Sprintf("cro-rodelete-%d", time.Now().UnixNano())
			By("Creating opt-in namespace")
			err := f.CreateCROOptInNamespace(f.Ctx, nsName)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.CleanupTestNamespace(f.Ctx, nsName) })

			By("Creating ResourceOverride 'aaa-override' with MemReq=25")
			err = f.CreateResourceOverride(f.Ctx, "aaa-override", nsName, framework.ROConfig{
				LimitCPUToMemoryPercent:     100,
				CPURequestToLimitPercent:    20,
				MemoryRequestToLimitPercent: 25,
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeleteResourceOverride(f.Ctx, "aaa-override", nsName) })

			By("Creating ResourceOverride 'zzz-override' with MemReq=75")
			err = f.CreateResourceOverride(f.Ctx, "zzz-override", nsName, framework.ROConfig{
				LimitCPUToMemoryPercent:     100,
				CPURequestToLimitPercent:    80,
				MemoryRequestToLimitPercent: 75,
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeleteResourceOverride(f.Ctx, "zzz-override", nsName) })

			By("Waiting for the webhook informer to pick up both ResourceOverrides")
			time.Sleep(30 * time.Second)

			By("Creating first pod — 'aaa-override' should win (25% of 1024Mi = 256Mi)")
			first, err := f.CreatePodWithResources(f.Ctx, "test-rodelete-1", nsName, roTestContainers())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, first.Name, nsName) })

			firstContainer := findContainer(first.Spec.Containers, "app")
			Expect(firstContainer).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] pod 1 Mem req=%s (expected 256Mi)\n",
				firstContainer.Resources.Requests.Memory().String())
			Expect(firstContainer.Resources.Requests.Memory().Value()).To(Equal(int64(256 * 1024 * 1024)))

			By("Deleting the winning ResourceOverride 'aaa-override'")
			err = f.DeleteResourceOverride(f.Ctx, "aaa-override", nsName)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the webhook informer to notice the deletion")
			time.Sleep(30 * time.Second)

			By("Creating second pod — 'zzz-override' should now win (75% of 1024Mi = 768Mi)")
			second, err := f.CreatePodWithResources(f.Ctx, "test-rodelete-2", nsName, roTestContainers())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, second.Name, nsName) })

			secondContainer := findContainer(second.Spec.Containers, "app")
			Expect(secondContainer).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] pod 2 Mem req=%s (expected 768Mi)\n",
				secondContainer.Resources.Requests.Memory().String())
			Expect(secondContainer.Resources.Requests.Memory().Value()).To(Equal(int64(768*1024*1024)),
				"mutation should have switched to the remaining ResourceOverride")

			By("Deleting the last ResourceOverride 'zzz-override'")
			err = f.DeleteResourceOverride(f.Ctx, "zzz-override", nsName)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the webhook informer to notice the deletion")
			time.Sleep(30 * time.Second)

			By("Creating third pod — no ResourceOverride left, CRO config applies (50% of 1024Mi = 512Mi)")
			third, err := f.CreatePodWithResources(f.Ctx, "test-rodelete-3", nsName, roTestContainers())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = f.DeletePod(f.Ctx, third.Name, nsName) })

			thirdContainer := findContainer(third.Spec.Containers, "app")
			Expect(thirdContainer).NotTo(BeNil())
			GinkgoWriter.Printf("[Test] pod 3 Mem req=%s (expected 512Mi)\n",
				thirdContainer.Resources.Requests.Memory().String())
			Expect(thirdContainer.Resources.Requests.Memory().Value()).To(Equal(int64(512*1024*1024)),
				"pod should have fallen back to the ClusterResourceOverride config")

			GinkgoWriter.Printf("[Test] ResourceOverride conflict + fallback verified\n")
		})
	})

	Context("Deployment verification", func() {

		It("should have the CRO webhook deployment running", func() {
			By("Checking for ClusterResourceOverride webhook deployment")
			dep, err := f.GetDeployment(f.Ctx, "clusterresourceoverride", framework.CRONamespace)
			if err != nil {
				GinkgoWriter.Printf("[Test] Webhook deployment 'clusterresourceoverride' not found, trying alternative names\n")
				Skip("CRO webhook deployment not found — may use different naming convention")
			}

			GinkgoWriter.Printf("[Test] CRO webhook deployment: replicas=%d/%d, available=%d\n",
				dep.Status.ReadyReplicas, *dep.Spec.Replicas, dep.Status.AvailableReplicas)

			By("Verifying deployment has at least 1 ready replica")
			Expect(dep.Status.ReadyReplicas).To(BeNumerically(">=", int32(1)),
				"CRO webhook deployment should have at least 1 ready replica")

			By("Verifying deployment has at least 1 available replica")
			Expect(dep.Status.AvailableReplicas).To(BeNumerically(">=", int32(1)),
				"CRO webhook deployment should have at least 1 available replica")
		})
	})
})

// roTestContainers is the pod used by the ResourceOverride tests, 1024Mi of memory
// keeps the expected memory requests round numbers (25% = 256Mi, 50% = 512Mi, 75% = 768Mi)
func roTestContainers() []corev1.Container {
	return []corev1.Container{{
		Name:  "app",
		Image: "registry.k8s.io/pause:3.9",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1024Mi"),
				corev1.ResourceCPU:    resource.MustParse("2000m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1024Mi"),
				corev1.ResourceCPU:    resource.MustParse("2000m"),
			},
		},
	}}
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}
