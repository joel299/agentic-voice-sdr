#ifndef GRU151_BARESIP_COMPAT_H
#define GRU151_BARESIP_COMPAT_H
#include <re.h>
/* Some libre versions omit the enum used in Baresip 1.1.0's public config.
 * The build probes the dependency headers rather than redefining it blindly.
 * It is not accessed by this module; the integer ABI layout is unchanged. */
#ifdef GRU151_DECLARE_JBUF_TYPE
enum jbuf_type { JBUF_OFF = 0, JBUF_FIXED, JBUF_ADAPTIVE };
#endif
#endif
