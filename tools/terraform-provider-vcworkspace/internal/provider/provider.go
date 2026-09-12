package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &vcWorkspaceProvider{}

type vcWorkspaceProvider struct {
	version string
}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIToken types.String `tfsdk:"api_token"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &vcWorkspaceProvider{version: version} }
}

func (p *vcWorkspaceProvider) Metadata(_ context.Context, _ provider.MetadataRequest, response *provider.MetadataResponse) {
	response.TypeName = "vcworkspace"
	response.Version = p.version
}

func (p *vcWorkspaceProvider) Schema(_ context.Context, _ provider.SchemaRequest, response *provider.SchemaResponse) {
	response.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"endpoint":  schema.StringAttribute{Optional: true, Description: "VC Workspace public control-plane URL. Defaults to VC_WORKSPACE_ENDPOINT."},
		"api_token": schema.StringAttribute{Optional: true, Sensitive: true, Description: "A vcwi_ administrator API credential. Defaults to VC_WORKSPACE_API_TOKEN."},
	}}
}

func (p *vcWorkspaceProvider) Configure(ctx context.Context, request provider.ConfigureRequest, response *provider.ConfigureResponse) {
	var configuration providerModel
	response.Diagnostics.Append(request.Config.Get(ctx, &configuration)...)
	if response.Diagnostics.HasError() {
		return
	}
	endpoint := os.Getenv("VC_WORKSPACE_ENDPOINT")
	if !configuration.Endpoint.IsNull() && !configuration.Endpoint.IsUnknown() {
		endpoint = configuration.Endpoint.ValueString()
	}
	token := os.Getenv("VC_WORKSPACE_API_TOKEN")
	if !configuration.APIToken.IsNull() && !configuration.APIToken.IsUnknown() {
		token = configuration.APIToken.ValueString()
	}
	configured, err := newClient(endpoint, token)
	if err != nil {
		response.Diagnostics.Append(diag.NewErrorDiagnostic("Invalid VC Workspace provider configuration", err.Error()))
		return
	}
	response.DataSourceData = configured
	response.ResourceData = configured
}

func (p *vcWorkspaceProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{newDesktopAssignmentResource}
}

func (p *vcWorkspaceProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{newDesktopsDataSource}
}
