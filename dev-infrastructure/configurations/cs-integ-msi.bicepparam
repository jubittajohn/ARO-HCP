using '../templates/cs-integration-msi.bicep'

param namespaceFormatString = 'sandbox-jenkins-{0}-aro-hcp'

param clusterServiceManagedIdentityName = 'cs-integ-mgmt-cluster'

param clusterName = take('cs-integ-svc-cluster-${uniqueString('svc-cluster')}', 63)
