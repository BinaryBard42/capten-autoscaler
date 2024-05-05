package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"intelops-scaler/pkg/cloudprovider/azure"
	"intelops-scaler/pkg/vault"

	//"fmt"
	//"intelops-scaler/pkg/cloudprovider/azure"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/informers"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"intelops-scaler/pkg/autoscaler"
	//"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
)

// Generated from example definition: https://github.com/Azure/azure-rest-api-specs/blob/60679ee3db06e93eb73faa0587fed93ed843d6dc/specification/compute/resource-manager/Microsoft.Compute/ComputeRP/stable/2023-09-01/examples/virtualMachineExamples/VirtualMachine_Get_WithDiskControllerType.json
func main() {
	/*
		azureClient := azure.New()
		res, err := azureClient.clientFactory.NewVirtualMachineScaleSetsClient().Get(ctx, "talosworker_group", "talosworker", &armcompute.VirtualMachineScaleSetsClientGetOptions{Expand: nil})
		if err != nil {
			log.Fatalf("failed to finish the request: %v", err)
		}

		fmt.Println(*res.SKU.Capacity)
		// You could use response here. We use blank identifier for just demo purposes.
		fmt.Printf("%+v", res)
		*res.SKU.Capacity += 1
		req := armcompute.VirtualMachineScaleSetUpdate{
			Identity:   nil,
			Plan:       nil,
			Properties: nil,
			SKU:        res.SKU,
			Tags:       nil,
		}
		updateRes, err := clientFactory.NewVirtualMachineScaleSetsClient().BeginUpdate(ctx, "talosworker_group", "talosworker", req, nil)
		if err != nil {
			log.Fatalf("failed to update the scale set: %v", err)
		}

		fmt.Printf("%+v", updateRes)

	*/
	//setupAutoscaler()
	resp, err := vault.GetGenericCredential(context.Background(), "azsecret", "azsecret")
	if err != nil {
		log.Fatalf("failed to connect vault")
	}

	fmt.Println(resp)
}

func setupAutoscaler() {
	// Informer transform to trim ManagedFields for memory efficiency.
	trim := func(obj interface{}) (interface{}, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			accessor.SetManagedFields(nil)
		}
		return obj, nil
	}

	kubeClient, err := getKubeConfig()
	if err != nil {
		log.Fatalf("failed to initialise the kubeclient: %v", err)
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, 0, informers.WithTransform(trim))
	nodeInfo := autoscaler.NodeInfo{
		CPU:    2,
		Memory: 7816684000,
		Min:    1,
		Max:    3,
	}

	azureClient, err := azure.New(os.Getenv("AZURE_SUBSCRIPTION_ID"))
	if err != nil {
		log.Fatalf("failed to create the azure client: %v", err)
	}

	autoscalerObj := autoscaler.NewAutoscaler(kubeClient, informerFactory, nodeInfo, azureClient)
	stopChan := make(chan struct{})
	informerFactory.Start(stopChan)

	ctx, ctxCancelFunc := context.WithCancel(context.Background())
	go autoscalerObj.Start(ctx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	<-sigChan
	ctxCancelFunc()
	close(stopChan)
}

func getKubeConfig() (*kubernetes.Clientset, error) {
	k8sConfig, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, err
	}

	return clientset, nil
}
