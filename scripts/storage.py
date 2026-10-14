#!/usr/bin/env python3
"""Shared durable writes and checksummed recovery for the shell backend."""
import base64, hashlib, json, os, pathlib, sys, tempfile, subprocess, shlex, re, stat

MAX_BYTES=4*1024*1024

def read_bounded(path,limit=MAX_BYTES):
    with pathlib.Path(path).open('rb') as handle:
        info=os.fstat(handle.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size>limit: raise ValueError('configuration is not a bounded regular file')
        data=handle.read(limit+1)
        if len(data)>limit: raise ValueError('configuration is oversized')
        return data

def environment_values(data):
    result={}
    for line in data.decode().splitlines():
        line=line.strip()
        if not line or line.startswith('#'): continue
        key,separator,value=line.partition('=')
        if not separator or not re.fullmatch(r'[A-Z][A-Z0-9_]*',key): raise ValueError('invalid environment assignment')
        if key in result: raise ValueError('duplicate environment key: '+key)
        quote=None;escaped=False
        for index,char in enumerate(value):
            if escaped: escaped=False;continue
            if char=='\\' and quote!="'": escaped=True;continue
            if quote:
                if char==quote: quote=None
                elif quote=='"' and char in '$`': raise ValueError('environment contains executable syntax')
                continue
            if char in "\x27\x22": quote=char;continue
            if char=='$' and value[index:index+2]=="$'": continue
            if char in '$`;|&<>()': raise ValueError('environment contains executable syntax')
        words=shlex.split(value)
        if len(words)!=1: raise ValueError('environment value must be one escaped shell word')
        result[key]=words[0]
    return result

def validate(path,data):
    if len(data)>4*1024*1024 or not data.strip(): raise ValueError('empty/oversized configuration')
    if pathlib.Path(path).name=='dual-protocol-script-fix':
        values=environment_values(data)
        if not 10240<=int(values.get('WEB_PORT','0'))<=65535 or not 1<=int(values.get('MAX_EXITS','0'))<=200 or not values.get('PANEL_DOMAIN') or not values.get('TLS_CERT') or not values.get('TLS_KEY'):
            raise ValueError('panel environment has missing/invalid fields')
    if pathlib.Path(path).name=='state.env':
        values=environment_values(data)
        mode=values.get('MAIN_MODE');stack=values.get('STACK_MODE')
        if (mode,stack) not in [('xhttp','xhttp_hy2'),('reality','reality_hy2')]: raise ValueError('unsupported/incomplete node state')
        required=['NODE_NAME','FP','HY2_DOMAIN','HY2_PASSWORD','HY2_PORT_MODE','HY2_PORT']
        required+=['XHTTP_DOMAIN','XHTTP_PATH','XHTTP_UUID','XHTTP_BACKEND_PORT'] if mode=='xhttp' else ['REALITY_ADDRESS','REALITY_SNI','REALITY_UUID','REALITY_PRIVATE_KEY','REALITY_PUBLIC_KEY','REALITY_SHORT_ID','HY2_ACME_EMAIL']
        if any(not values.get(key) for key in required): raise ValueError('node state has missing active fields')
    if str(path).endswith('.json'):
        obj=json.loads(data)
        if pathlib.Path(path).name=='config.json' and (not isinstance(obj,dict) or not isinstance(obj.get('inbounds'),list) or not obj['inbounds']):
            raise ValueError('Xray inbounds missing')
    if str(path).endswith(('.yaml','.yml')):
        import yaml
        class UniqueLoader(yaml.SafeLoader): pass
        def mapping(loader,node,deep=False):
            result={}
            for key,value in node.value:
                k=loader.construct_object(key,deep=deep)
                if k in result: raise ValueError('duplicate YAML key: '+str(k))
                result[k]=loader.construct_object(value,deep=deep)
            return result
        UniqueLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,mapping)
        try: obj=yaml.load(data,Loader=UniqueLoader)
        except yaml.YAMLError as exc: raise ValueError('invalid YAML configuration') from exc
        if pathlib.Path(path).name=='config.yaml' and (not isinstance(obj,dict) or not obj.get('listen') or not obj.get('auth')):
            raise ValueError('HY2 listen/auth missing')

def atomic(path, data, mode):
    path = pathlib.Path(path)
    service={'/usr/local/etc/xray/config.json':'xray','/etc/hysteria/config.yaml':'hysteria-server'}.get(str(path))
    if service: mode=0o600
    fd, name = tempfile.mkstemp(prefix='.'+path.name+'.tmp-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as f:
            if os.name != 'nt': os.fchmod(f.fileno(), mode)
            f.write(data); f.flush(); os.fsync(f.fileno())
        if service and os.name != 'nt':
            user=subprocess.check_output(['systemctl','show',service,'-p','User','--value'],text=True).strip()
            if user and user!='root': subprocess.run(['setfacl','-m','u:'+user+':r',name],check=True,capture_output=True)
        os.replace(name, path)
        if os.name != 'nt':
            d = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try: os.fsync(d)
            finally: os.close(d)
    finally:
        if os.path.exists(name): os.unlink(name)

def checkpoint(path, data):
    validate(path,data)
    record = dict(path=str(path), sha256=hashlib.sha256(data).hexdigest(), data=base64.b64encode(data).decode())
    raw = json.dumps(record).encode()
    backup = pathlib.Path(str(path)+'.fix-recovery')
    if backup.exists():
        try:
            if json.loads(read_bounded(backup,MAX_BYTES*2)) == record: return
        except (OSError, ValueError): pass
    atomic(backup, raw, 0o600)

def recover(path):
    try:
        data=read_bounded(path)
        validate(path,data)
    except FileNotFoundError: raise
    except (OSError,ValueError) as cause:
        record=json.loads(read_bounded(pathlib.Path(str(path)+'.fix-recovery'),MAX_BYTES*2))
        data=base64.b64decode(record['data'],validate=True)
        if record['path']!=str(path) or hashlib.sha256(data).hexdigest()!=record['sha256']:
            raise ValueError('recovery checksum/path mismatch') from cause
        validate(path,data)
        atomic(path,data,path.stat().st_mode & 0o777 if path.exists() else 0o600)
        if read_bounded(path)!=data: raise OSError('recovery readback failed')
        print('已恢复配置文件，请检查服务器磁盘/文件系统: '+str(path),file=sys.stderr)
    else:
        # Backup failure is not evidence that the healthy primary is corrupt.
        # Report it without replacing the primary with an older snapshot.
        try: checkpoint(path,data)
        except (OSError,ValueError) as exc:
            print('主配置已验证可读，但恢复副本更新失败: '+str(path)+': '+str(exc),file=sys.stderr)

def main():
    command=sys.argv[1]; path=pathlib.Path(sys.argv[2])
    if command=='write':
        data=read_bounded(pathlib.Path(sys.argv[3]));mode=int(sys.argv[4],8)
        validate(path,data)
        atomic(path,data,mode)
        if read_bounded(path)!=data: raise OSError('write readback failed')
        checkpoint(path,data)
    elif command=='recover': recover(path)
    elif command=='checkpoint': checkpoint(path,read_bounded(path))
    else: raise ValueError('unknown storage command')
if __name__=='__main__':
    try: main()
    except Exception as exc:
        print('配置存储失败，未报告成功: '+str(exc),file=sys.stderr);sys.exit(1)
