package provider

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &desktopAssignmentResource{}
	_ resource.ResourceWithConfigure   = &desktopAssignmentResource{}
	_ resource.ResourceWithImportState = &desktopAssignmentResource{}
)

type desktopAssignmentResource struct {
	client *client
}

type desktopAssignmentModel struct {
	ID          types.String `tfsdk:"id"`
	SubjectType types.String `tfsdk:"subject_type"`
	SubjectID   types.String `tfsdk:"subject_id"`
	DesktopVMID types.Int64  `tfsdk:"desktop_vmid"`
}

func newDesktopAssignmentResource() resource.Resource { return &desktopAssignmentResource{} }

func (r *desktopAssignmentResource) Metadata(_ context.Context, request resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = request.ProviderTypeName + "_desktop_assignment"
}

func (r *desktopAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse) {
	response.Schema = schema.Schema{
		Description: "Assigns a managed desktop to a platform user, local/OIDC group, or AI agent.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true},
			"subject_type": schema.StringAttribute{Required: true, Description: "user, group, or agent", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"subject_id":   schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"desktop_vmid": schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		},
	}
}

func (r *desktopAssignmentResource) Configure(_ context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse) {
	if request.ProviderData == nil {
		return
	}
	configured, ok := request.ProviderData.(*client)
	if !ok {
		response.Diagnostics.AddError("Unexpected provider configuration", fmt.Sprintf("Expected *client, got %T", request.ProviderData))
		return
	}
	r.client = configured
}

func assignmentFromModel(model desktopAssignmentModel) assignment {
	return assignment{SubjectType: model.SubjectType.ValueString(), SubjectID: model.SubjectID.ValueString(), DesktopVMID: model.DesktopVMID.ValueInt64()}
}

func assignmentID(value assignment) string {
	return value.SubjectType + "/" + url.PathEscape(value.SubjectID) + "/" + strconv.FormatInt(value.DesktopVMID, 10)
}

func (r *desktopAssignmentResource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	var plan desktopAssignmentModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	value := assignmentFromModel(plan)
	if err := validateAssignment(value); err != nil {
		response.Diagnostics.AddError("Invalid desktop assignment", err.Error())
		return
	}
	if err := r.client.putAssignment(ctx, value); err != nil {
		response.Diagnostics.AddError("Unable to assign desktop", err.Error())
		return
	}
	plan.ID = types.StringValue(assignmentID(value))
	response.Diagnostics.Append(response.State.Set(ctx, plan)...)
}

func (r *desktopAssignmentResource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	var state desktopAssignmentModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	wanted := assignmentFromModel(state)
	current, err := r.client.accessControl(ctx)
	if err != nil {
		response.Diagnostics.AddError("Unable to read desktop assignment", err.Error())
		return
	}
	for _, candidate := range current.Assignments {
		if candidate.SubjectType == wanted.SubjectType && candidate.SubjectID == wanted.SubjectID && candidate.DesktopVMID == wanted.DesktopVMID {
			state.ID = types.StringValue(assignmentID(candidate))
			response.Diagnostics.Append(response.State.Set(ctx, state)...)
			return
		}
	}
	response.State.RemoveResource(ctx)
}

func (r *desktopAssignmentResource) Update(ctx context.Context, request resource.UpdateRequest, response *resource.UpdateResponse) {
	var plan desktopAssignmentModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	response.Diagnostics.Append(response.State.Set(ctx, plan)...)
}

func (r *desktopAssignmentResource) Delete(ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse) {
	var state desktopAssignmentModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	if err := r.client.deleteAssignment(ctx, assignmentFromModel(state)); err != nil {
		response.Diagnostics.AddError("Unable to remove desktop assignment", err.Error())
	}
}

func (r *desktopAssignmentResource) ImportState(ctx context.Context, request resource.ImportStateRequest, response *resource.ImportStateResponse) {
	parts := strings.Split(request.ID, "/")
	if len(parts) != 3 {
		response.Diagnostics.AddError("Invalid import identifier", "Use subject_type/URL-escaped-subject_id/desktop_vmid.")
		return
	}
	subjectID, err := url.PathUnescape(parts[1])
	if err != nil {
		response.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	vmid, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		response.Diagnostics.AddError("Invalid import identifier", "desktop_vmid must be a positive integer.")
		return
	}
	value := assignment{SubjectType: parts[0], SubjectID: subjectID, DesktopVMID: vmid}
	if err := validateAssignment(value); err != nil {
		response.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("id"), assignmentID(value))...)
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("subject_type"), value.SubjectType)...)
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("subject_id"), value.SubjectID)...)
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("desktop_vmid"), value.DesktopVMID)...)
}

func validateAssignment(value assignment) error {
	if value.SubjectType != "user" && value.SubjectType != "group" && value.SubjectType != "agent" {
		return fmt.Errorf("subject_type must be user, group, or agent")
	}
	if strings.TrimSpace(value.SubjectID) == "" {
		return fmt.Errorf("subject_id is required")
	}
	if strings.Contains(value.SubjectID, "/") {
		return fmt.Errorf("subject_id cannot contain a slash")
	}
	if value.DesktopVMID <= 0 {
		return fmt.Errorf("desktop_vmid must be positive")
	}
	return nil
}
