package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &desktopsDataSource{}
	_ datasource.DataSourceWithConfigure = &desktopsDataSource{}
)

type desktopsDataSource struct{ client *client }

type desktopModel struct {
	VMID        types.Int64  `tfsdk:"vmid"`
	DisplayName types.String `tfsdk:"display_name"`
	Node        types.String `tfsdk:"node"`
	OSFamily    types.String `tfsdk:"os_family"`
	Present     types.Bool   `tfsdk:"present"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	AccessMode  types.String `tfsdk:"access_mode"`
}

type desktopsDataSourceModel struct {
	Desktops []desktopModel `tfsdk:"desktops"`
}

func newDesktopsDataSource() datasource.DataSource { return &desktopsDataSource{} }

func (d *desktopsDataSource) Metadata(_ context.Context, request datasource.MetadataRequest, response *datasource.MetadataResponse) {
	response.TypeName = request.ProviderTypeName + "_desktops"
}

func (d *desktopsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, response *datasource.SchemaResponse) {
	response.Schema = schema.Schema{Description: "Lists managed desktops visible to the IaC administrator.", Attributes: map[string]schema.Attribute{
		"desktops": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"vmid": schema.Int64Attribute{Computed: true}, "display_name": schema.StringAttribute{Computed: true}, "node": schema.StringAttribute{Computed: true},
			"os_family": schema.StringAttribute{Computed: true}, "present": schema.BoolAttribute{Computed: true}, "enabled": schema.BoolAttribute{Computed: true}, "access_mode": schema.StringAttribute{Computed: true},
		}}},
	}}
}

func (d *desktopsDataSource) Configure(_ context.Context, request datasource.ConfigureRequest, response *datasource.ConfigureResponse) {
	if request.ProviderData == nil {
		return
	}
	configured, ok := request.ProviderData.(*client)
	if !ok {
		response.Diagnostics.AddError("Unexpected provider configuration", fmt.Sprintf("Expected *client, got %T", request.ProviderData))
		return
	}
	d.client = configured
}

func (d *desktopsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, response *datasource.ReadResponse) {
	current, err := d.client.accessControl(ctx)
	if err != nil {
		response.Diagnostics.AddError("Unable to read desktops", err.Error())
		return
	}
	state := desktopsDataSourceModel{Desktops: make([]desktopModel, 0, len(current.Desktops))}
	for _, item := range current.Desktops {
		state.Desktops = append(state.Desktops, desktopModel{VMID: types.Int64Value(item.VMID), DisplayName: types.StringValue(item.DisplayName), Node: types.StringValue(item.Node), OSFamily: types.StringValue(item.OSFamily), Present: types.BoolValue(item.Present), Enabled: types.BoolValue(item.Enabled), AccessMode: types.StringValue(item.AccessMode)})
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}
