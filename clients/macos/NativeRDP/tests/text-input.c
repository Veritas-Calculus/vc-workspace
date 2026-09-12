#include "../VCWTextInput.h"
#include <assert.h>
#include <stdio.h>

struct Fixture { size_t count, stopAt, failAt; uint16_t codes[8192]; bool releases[8192]; };
static bool current(void *context) {
    struct Fixture *f = context;
    return f->count < f->stopAt;
}
static bool emit(void *context, uint16_t code, bool release) {
    struct Fixture *f = context;
    assert(f->count < 8192);
    f->codes[f->count] = code;
    f->releases[f->count] = release;
    return f->count++ != f->failAt;
}
int main(void) {
    const uint16_t text[] = {0x4e2d, 0x6587, '|', '\\', 0xd83d, 0xde00, 'e', 0x301};
    const size_t length = sizeof(text)/sizeof(text[0]);
    struct Fixture f = {.stopAt=SIZE_MAX, .failAt=SIZE_MAX};
    assert(vcw_send_committed_text(text,length,&f,current,emit)==VCWTextSent);
    assert(f.count==length*2);
    for (size_t i=0;i<f.count;i++) assert(f.codes[i]==text[i/2] && f.releases[i]==(i%2!=0));
    for (size_t stop=0;stop<length*2;stop++) {
        f=(struct Fixture){.stopAt=stop,.failAt=SIZE_MAX};
        assert(vcw_send_committed_text(text,length,&f,current,emit)==VCWTextUnavailable);
        assert(f.count==stop);
        f=(struct Fixture){.stopAt=SIZE_MAX,.failAt=stop};
        assert(vcw_send_committed_text(text,length,&f,current,emit)==VCWTextFailed);
        assert(f.count==stop+1);
    }
    const uint16_t invalid[][3]={{'a',0,'b'},{'a',0xd800,'b'},{'a',0xdc00,'b'},
        {'a',0xdfff,'b'},{'a','\n','b'},{'a',0x7f,'b'},{'a','b',0xdbff}};
    for(size_t i=0;i<sizeof(invalid)/sizeof(invalid[0]);i++) {
        f=(struct Fixture){.stopAt=SIZE_MAX,.failAt=SIZE_MAX};
        assert(vcw_send_committed_text(invalid[i],3,&f,current,emit)==VCWTextInvalid && !f.count);
    }
    uint16_t boundary[4097];
    for(size_t i=0;i<4097;i++) boundary[i]='a';
    f=(struct Fixture){.stopAt=SIZE_MAX,.failAt=SIZE_MAX};
    assert(vcw_send_committed_text(boundary,4097,&f,current,emit)==VCWTextInvalid && !f.count);
    assert(vcw_send_committed_text(boundary,4096,&f,current,emit)==VCWTextSent && f.count==8192);
    assert(vcw_send_committed_text(NULL,1,&f,current,emit)==VCWTextInvalid);
    assert(vcw_send_committed_text(text,0,&f,current,emit)==VCWTextInvalid);
    assert(vcw_send_committed_text(text,length,&f,NULL,emit)==VCWTextInvalid);
    assert(vcw_send_committed_text(text,length,&f,current,NULL)==VCWTextInvalid);
    puts("text input: UTF-16, bounds, focus loss and partial-send failures passed");
}
