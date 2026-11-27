// Package terraform holds the types for what Terraform provisioned: the values
// Launchpad's tf/ outputs hand to the server, and the phases the pipeline asks
// its Provision step for. The server runs no Terraform itself.
package terraform

import "log/slog"

// Outputs are the values PIMS needs from the provisioned resources.
type Outputs struct {
	AWSRegion                string   `json:"aws_region"`
	OrganizationID           string   `json:"organization_id"`
	EnvironmentID            string   `json:"environment_id"`
	KafkaClusterID           string   `json:"kafka_cluster_id"`
	KafkaBootstrap           string   `json:"kafka_bootstrap"`
	KafkaAPIKey              string   `json:"kafka_api_key"`
	KafkaAPISecret           string   `json:"kafka_api_secret"`
	SchemaRegistryURL        string   `json:"schema_registry_url"`
	SchemaRegistryAPIKey     string   `json:"schema_registry_api_key"`
	SchemaRegistryAPISecret  string   `json:"schema_registry_api_secret"`
	TableflowAPIKey          string   `json:"tableflow_api_key"`
	TableflowAPISecret       string   `json:"tableflow_api_secret"`
	TableflowCatalogEndpoint string   `json:"tableflow_catalog_endpoint"`
	Topics                   []string `json:"topics"`
	TableflowEnabled         bool     `json:"tableflow_enabled"`
}

// LogValue keeps generated secrets out of logs.
func (o Outputs) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("environment_id", o.EnvironmentID),
		slog.String("kafka_cluster_id", o.KafkaClusterID),
		slog.String("kafka_bootstrap", o.KafkaBootstrap),
		slog.String("schema_registry_url", o.SchemaRegistryURL),
		slog.Any("topics", o.Topics),
		slog.Bool("tableflow_enabled", o.TableflowEnabled),
	)
}

// ApplyOptions select which phase of the provisioning the pipeline asks for.
type ApplyOptions struct {
	// EnableTableflow turns on Tableflow for the CDC topics. Only set it
	// after the topic schemas have been registered.
	EnableTableflow bool
}
