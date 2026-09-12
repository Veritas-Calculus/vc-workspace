/* Keep private RDP headers out of AppKit's CUPS HTTP enum namespace. */
#include "rdp.h"

void vcw_test_set_active(rdpContext *context, BOOL active)
{
	context->rdp->state = active ? CONNECTION_STATE_ACTIVE : CONNECTION_STATE_INITIAL;
}
