package kor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/utils/strings/slices"
)

func CheckFinalizers(finalizers []string, deletionTimestamp *metav1.Time) bool {
	if len(finalizers) > 0 && deletionTimestamp != nil {
		return true
	}
	return false
}

func getResourcesWithFinalizersPendingDeletion(clientset kubernetes.Interface, dynamicClient dynamic.Interface, namespaces []string, filterOpts *FilterOptions) (map[string]map[string][]string, error) {
	pendingDeletionResources := make(map[string]map[string][]string)

	// Use the discovery client to fetch API resources
	resourceTypes, err := clientset.Discovery().ServerPreferredResources()
	if err != nil {
		fmt.Printf("Error fetching server resources: %v\n", err)
		os.Exit(1)
	}

	for _, apiResourceList := range resourceTypes {
		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			return pendingDeletionResources, err
		}

		for _, resourceType := range apiResourceList.APIResources {
			if resourceType.Namespaced && slices.Contains(resourceType.Verbs, "list") {
				resourceList, err := dynamicClient.Resource(gv.WithResource(resourceType.Name)).Namespace(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					fmt.Printf("Error listing resources for GVR %s: %v\n", apiResourceList.GroupVersion, err)
					continue
				}
				for _, item := range resourceList.Items {

					labels := item.GetLabels()
					if labels["kor/used"] == "true" {
						continue
					}

					// Check for excluded labels
					if excluded, _ := HasExcludedLabel(labels, filterOpts.ExcludeLabels); excluded {
						continue
					}

					// Check age criteria
					if included, _ := HasIncludedAge(item.GetCreationTimestamp(), filterOpts); !included {
						continue
					}

					if CheckFinalizers(item.GetFinalizers(), item.GetDeletionTimestamp()) {
						if pendingDeletionResources[item.GetNamespace()] == nil {
							pendingDeletionResources[item.GetNamespace()] = make(map[string][]string)
						}
						pendingDeletionResources[item.GetNamespace()][resourceType.Name] = append(pendingDeletionResources[item.GetNamespace()][resourceType.Name], item.GetName())
					}
				}
			}
		}
	}

	return pendingDeletionResources, nil
}

func getNamespacedResourcesWithFinalizersPendingDeletion(clientset kubernetes.Interface, dynamicClient dynamic.Interface, namespace string, filterOpts *FilterOptions) (map[string][]string, error) {
	pendingDeletionResources := make(map[string][]string)
	// Use the discovery client to fetch API resources
	resourceTypes, err := clientset.Discovery().ServerPreferredResources()
	if err != nil {
		fmt.Printf("Error fetching server resources: %v\n", err)
		os.Exit(1)
	}

	for _, apiResourceList := range resourceTypes {
		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			return pendingDeletionResources, err
		}
		for _, resourceType := range apiResourceList.APIResources {
			if resourceType.Namespaced && slices.Contains(resourceType.Verbs, "list") {
				resourceList, err := dynamicClient.Resource(gv.WithResource(resourceType.Name)).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})

				if err != nil {
					fmt.Printf("Error listing resources for GVR %s: %v\n", apiResourceList.GroupVersion, err)
					continue
				}
				for _, item := range resourceList.Items {
					labels := item.GetLabels()
					if labels["kor/used"] == "true" {
						continue
					}

					// Check for excluded labels
					if excluded, _ := HasExcludedLabel(labels, filterOpts.ExcludeLabels); excluded {
						continue
					}

					// Check age criteria
					if included, _ := HasIncludedAge(item.GetCreationTimestamp(), filterOpts); !included {
						continue
					}

					if CheckFinalizers(item.GetFinalizers(), item.GetDeletionTimestamp()) {
						pendingDeletionResources[resourceType.Name] = append(pendingDeletionResources[resourceType.Name], item.GetName())
					}
				}
			}
		}
	}

	return pendingDeletionResources, nil
}

func GetUnusedfinalizers(includeExcludeLists IncludeExcludeLists, filterOpts *FilterOptions, clientset kubernetes.Interface, dynamicClient *dynamic.DynamicClient, outputFormat string, opts Opts) (string, error) {
	var outputBuffer bytes.Buffer
	namespaces := SetNamespaceList(includeExcludeLists, clientset)
	response := make(map[string]map[string][]string)
	if len(includeExcludeLists.ExcludeListStr) == 0 && len(includeExcludeLists.IncludeListStr) == 0 {
		resourceDiffs, err := getResourcesWithFinalizersPendingDeletion(clientset, dynamicClient, namespaces, filterOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to process resources waiting for finalizers: %v\n", err)
		}
		for namespace, data := range resourceDiffs {
			if slices.Contains(namespaces, namespace) {
				for resourceType, resourceDiff := range data {
					if opts.DeleteFlag {
						if resourceDiff, err = DeleteResourceWithFinalizer(resourceDiff, clientset, dynamicClient, namespace, resourceType, opts.NoInteractive); err != nil {
							fmt.Fprintf(os.Stderr, "Failed to delete objects waiting for Finalizers %s in namespace %s: %v\n", resourceDiff, namespace, err)
						}
					}
				}
				output := FormatOutputFromMap(namespace, data, opts)
				outputBuffer.WriteString(output)
				outputBuffer.WriteString("\n")

				response[namespace] = data
			}
		}
	} else {
		for _, namespace := range namespaces {
			resourceDiffs, err := getNamespacedResourcesWithFinalizersPendingDeletion(clientset, dynamicClient, namespace, filterOpts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to process namespace %s: %v\n", namespace, err)
				continue
			}
			for resourceType, resourceDiff := range resourceDiffs {
				if opts.DeleteFlag {
					if resourceDiff, err = DeleteResource(resourceDiff, clientset, namespace, resourceType, opts.NoInteractive); err != nil {
						fmt.Fprintf(os.Stderr, "Failed to delete objects waiting for Finalizers %s in namespace %s: %v\n", resourceDiff, namespace, err)
					}
				}
			}
			output := FormatOutputFromMap(namespace, resourceDiffs, opts)
			outputBuffer.WriteString(output)
			outputBuffer.WriteString("\n")

			response[namespace] = resourceDiffs
		}
	}

	jsonResponse, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return "", err
	}

	unusedFinalizers, err := unusedResourceFormatter(outputFormat, outputBuffer, opts, jsonResponse)
	if err != nil {
		fmt.Printf("err: %v\n", err)
	}

	return unusedFinalizers, nil
}
