package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/kasterism/astertower/pkg/apis/v1alpha1"
	astertowerclientset "github.com/kasterism/astertower/pkg/clients/clientset/astertower"
	informers "github.com/kasterism/astertower/pkg/clients/informer/externalversions/apis/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	// name of finalizer
	AstroFinalizer = "astros.astertower.kasterism.io"
)

type AstroController struct {
	kubeClientset kubernetes.Interface

	astroClientset astertowerclientset.Interface

	deploymentInformer appsinformers.DeploymentInformer

	serviceInformer coreinformers.ServiceInformer

	astroInformer informers.AstroInformer

	workqueue workqueue.RateLimitingInterface

	recorder record.EventRecorder
}

func NewAstroController(kubeClientset kubernetes.Interface, astroClientset astertowerclientset.Interface,
	deploymentInformer appsinformers.DeploymentInformer, serviceInformer coreinformers.ServiceInformer,
	astroInformer informers.AstroInformer) *AstroController {
	astroController := &AstroController{
		kubeClientset:      kubeClientset,
		astroClientset:     astroClientset,
		deploymentInformer: deploymentInformer,
		serviceInformer:    serviceInformer,
		astroInformer:      astroInformer,
		workqueue:          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "astro"),
	}

	klog.Infoln("Setting up Astro event handlers")

	_, err := astroInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    astroController.addAstro,
		DeleteFunc: astroController.deleteAstro,
		UpdateFunc: astroController.updateAstro,
	})
	if err != nil {
		klog.Fatalln("Failed to add event handlers")
	}

	return astroController
}

func (c *AstroController) Run(thread int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShuttingDown()

	klog.Infoln("Starting Astro control loop")

	klog.Infoln("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.astroInformer.Informer().HasSynced); !ok {
		return fmt.Errorf("failed to wati for caches to sync")
	}

	klog.Infoln("Starting workers")
	for i := 0; i < thread; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Infoln("Started workers")
	<-stopCh
	klog.Infoln("Shutting down workers")
	return nil
}

func (c *AstroController) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *AstroController) processNextWorkItem() bool {
	item, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}

	if err := func(item interface{}) error {
		defer c.workqueue.Done(item)
		var (
			key string
			ok  bool
		)
		if key, ok = item.(string); !ok {
			c.workqueue.Forget(item)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", item))
			return nil
		}
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s':%s", item, err.Error())
		}
		c.workqueue.Forget(item)
		return nil
	}(item); err != nil {
		runtime.HandleError(err)
		return false
	}
	return true
}

func (c *AstroController) syncHandler(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid respirce key:%s", key))
	}

	astro, err := c.astroInformer.Lister().Astros(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		runtime.HandleError(fmt.Errorf("failed to get astro by: %s/%s", namespace, name))
		return err
	}
	if !astro.DeletionTimestamp.IsZero() {
		return c.syncDelete(astro)
	}

	for _, finalizer := range astro.Finalizers {
		if finalizer == AstroFinalizer {
			return c.syncUpdate(astro)
		}
	}

	// TODO: do something
	return c.syncCreate(astro)
}

func (c *AstroController) addAstro(item interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(item); err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infoln("Enqueue the astro crd for adding")

	c.workqueue.AddRateLimited(key)
}

func (c *AstroController) deleteAstro(item interface{}) {
	var key string
	var err error
	if key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(item); err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infoln("Enqueue the astro crd for deleting")

	c.workqueue.AddRateLimited(key)
}

func (c *AstroController) updateAstro(old, new interface{}) {
	var key string
	var err error

	oldItem := old.(*v1alpha1.Astro)
	newItem := new.(*v1alpha1.Astro)
	if oldItem.ResourceVersion == newItem.ResourceVersion {
		return
	}

	if key, err = cache.MetaNamespaceKeyFunc(new); err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infoln("Enqueue the astro crd for updating")

	c.workqueue.AddRateLimited(key)
}

func (c *AstroController) syncCreate(astro *v1alpha1.Astro) error {
	klog.Infof("Sync create astro: %s\n", astro.Name)

	// Add finalizer when creating resources
	astro.Finalizers = append(astro.Finalizers, AstroFinalizer)

	_, err := c.astroClientset.AstertowerV1alpha1().Astros(astro.Namespace).Update(context.TODO(),
		astro, v1.UpdateOptions{})
	if err != nil {
		runtime.HandleError(err)
		return err
	}
	return nil
}

func (c *AstroController) syncUpdate(astro *v1alpha1.Astro) error {
	klog.Infof("Sync update astro: %s\n", astro.Name)

	return nil
}

func (c *AstroController) syncDelete(astro *v1alpha1.Astro) error {
	klog.Infof("Sync delete astro: %s\n", astro.Name)

	// Remove finalizer when deleting resources
	for i, finalizer := range astro.Finalizers {
		if finalizer == AstroFinalizer {
			astro.Finalizers[i] = astro.Finalizers[len(astro.Finalizers)-1]
			astro.Finalizers = astro.Finalizers[:len(astro.Finalizers)-1]
		}
	}

	_, err := c.astroClientset.AstertowerV1alpha1().Astros(astro.Namespace).Update(context.TODO(),
		astro, v1.UpdateOptions{})
	if err != nil {
		runtime.HandleError(err)
		return err
	}

	return nil
}
