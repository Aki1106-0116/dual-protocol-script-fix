#!/usr/bin/env python3
"""Remove only retired HY2 integration blocks, keeping all other settings."""
import pathlib, re
from storage import atomic, checkpoint, recover, validate

def remove_retired_obfuscation(state, config):
    state,config=pathlib.Path(state),pathlib.Path(config)
    if not state.exists(): return False
    recover(state);text=state.read_text()
    if not re.search(r'^HY2_OBFS_TYPE=(?:mimic|\x27mimic\x27|"mimic")$',text,re.M): return False
    recover(config);yaml=config.read_text()
    candidate=re.sub(r'^mimic:\s*\n(?:[ \t]+.*\n|\s*\n)*','',yaml,flags=re.M)
    if candidate==yaml: raise ValueError('旧状态启用了已移除混淆，但配置块无法识别；拒绝改动')
    # Keep explicit pre-migration files for administrator rollback.
    old_state=state.read_bytes();old_config=config.read_bytes()
    atomic(str(state)+'.before-fix',old_state,0o600)
    atomic(str(config)+'.before-fix',old_config,0o600)
    text=re.sub(r'^HY2_OBFS_TYPE=.*$', 'HY2_OBFS_TYPE=none',text,flags=re.M)
    validate(state,text.encode());validate(config,candidate.encode())
    try:
        atomic(config,candidate.encode(),config.stat().st_mode & 0o777)
        atomic(state,text.encode(),0o600)
        if config.read_bytes()!=candidate.encode() or state.read_bytes()!=text.encode(): raise OSError('migration readback failed')
        checkpoint(state,text.encode());checkpoint(config,candidate.encode())
    except Exception as cause:
        failures=[]
        for path,data in [(config,old_config),(state,old_state)]:
            try: atomic(path,data,0o600);checkpoint(path,data)
            except Exception as failure: failures.append(str(failure))
        if failures: raise OSError('migration rollback failed; before-fix backups preserved: '+'; '.join(failures)) from cause
        raise
    return True

if __name__=='__main__':
    if remove_retired_obfuscation('/etc/jb-combo/state.env','/etc/hysteria/config.yaml'):
        print('HY2_RESTART_REQUIRED=1')
