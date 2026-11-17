#include "textflag.h"

TEXT libc_sysctlbyname_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_sysctlbyname(SB)
GLOBL	·libc_sysctlbyname_trampoline_addr(SB), RODATA, $8
DATA	·libc_sysctlbyname_trampoline_addr(SB)/8, $libc_sysctlbyname_trampoline<>(SB)

TEXT libc_mach_host_self_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_mach_host_self(SB)
GLOBL	·libc_mach_host_self_trampoline_addr(SB), RODATA, $8
DATA	·libc_mach_host_self_trampoline_addr(SB)/8, $libc_mach_host_self_trampoline<>(SB)

TEXT libc_host_processor_info_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_host_processor_info(SB)
GLOBL	·libc_host_processor_info_trampoline_addr(SB), RODATA, $8
DATA	·libc_host_processor_info_trampoline_addr(SB)/8, $libc_host_processor_info_trampoline<>(SB)

TEXT libc_host_statistics64_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_host_statistics64(SB)
GLOBL	·libc_host_statistics64_trampoline_addr(SB), RODATA, $8
DATA	·libc_host_statistics64_trampoline_addr(SB)/8, $libc_host_statistics64_trampoline<>(SB)

TEXT libc_vm_deallocate_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_vm_deallocate(SB)
GLOBL	·libc_vm_deallocate_trampoline_addr(SB), RODATA, $8
DATA	·libc_vm_deallocate_trampoline_addr(SB)/8, $libc_vm_deallocate_trampoline<>(SB)

TEXT libc_task_self_trap_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_task_self_trap(SB)
GLOBL	·libc_task_self_trap_trampoline_addr(SB), RODATA, $8
DATA	·libc_task_self_trap_trampoline_addr(SB)/8, $libc_task_self_trap_trampoline<>(SB)

TEXT libc_proc_pidinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_pidinfo(SB)
GLOBL	·libc_proc_pidinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidinfo_trampoline_addr(SB)/8, $libc_proc_pidinfo_trampoline<>(SB)

TEXT libc_proc_listallpids_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_listallpids(SB)
GLOBL	·libc_proc_listallpids_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_listallpids_trampoline_addr(SB)/8, $libc_proc_listallpids_trampoline<>(SB)

TEXT libc_mach_timebase_info_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_mach_timebase_info(SB)
GLOBL	·libc_mach_timebase_info_trampoline_addr(SB), RODATA, $8
DATA	·libc_mach_timebase_info_trampoline_addr(SB)/8, $libc_mach_timebase_info_trampoline<>(SB)

