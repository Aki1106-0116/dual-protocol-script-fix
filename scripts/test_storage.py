import importlib.util, pathlib, tempfile, unittest, json
from unittest.mock import patch
import storage
from migrate import remove_retired_obfuscation

class StorageTests(unittest.TestCase):
    def test_interrupted_replace_keeps_original(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'state.env';p.write_bytes(b'old')
            with patch('storage.os.replace',side_effect=OSError('injected I/O error')):
                with self.assertRaises(OSError): storage.atomic(p,b'new',0o600)
            self.assertEqual(p.read_bytes(),b'old')
            self.assertEqual(list(pathlib.Path(folder).glob('.*.tmp-*')),[])

    def test_eio_recovers_verified_bytes(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'panel.env';p.write_bytes(b'KEY=value\n');storage.checkpoint(p,p.read_bytes())
            original=storage.read_bounded;calls=0
            def read(path,limit=storage.MAX_BYTES):
                nonlocal calls
                if path==p and calls==0: calls+=1;raise OSError(5,'Input/output error')
                return original(path,limit)
            with patch('storage.read_bounded',read):storage.recover(p)
            self.assertEqual(p.read_bytes(),b'KEY=value\n')

    def test_bad_json_cannot_overwrite_snapshot(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'config.json';p.write_bytes(b'{"inbounds":[{}]}');storage.checkpoint(p,p.read_bytes());backup=pathlib.Path(str(p)+'.fix-recovery').read_bytes()
            with self.assertRaises(ValueError):storage.checkpoint(p,b'{broken')
            self.assertEqual(pathlib.Path(str(p)+'.fix-recovery').read_bytes(),backup)

    def test_recovery_rejects_bad_checksum(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'state';p.write_bytes(b'');pathlib.Path(str(p)+'.fix-recovery').write_text(json.dumps(dict(path=str(p),sha256='invalid',data='eA==')))
            with self.assertRaises(ValueError):storage.recover(p)
            self.assertEqual(p.read_bytes(),b'')

    def test_missing_primary_is_not_resurrected(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'state';storage.checkpoint(p,b'old')
            with self.assertRaises(FileNotFoundError):storage.recover(p)

    def test_duplicate_yaml_is_rejected(self):
        with self.assertRaises(ValueError):storage.validate('config.yaml',b'listen: :1234\nauth: {}\nauth: {}\n')

    def test_migration_preserves_credentials_and_other_blocks(self):
        with tempfile.TemporaryDirectory() as folder:
            state=pathlib.Path(folder)/'state.env';config=pathlib.Path(folder)/'config.yaml'
            state.write_text('STACK_MODE=reality_hy2\nMAIN_MODE=reality\nNODE_NAME=fixture\nFP=chrome\nHY2_DOMAIN=h.example.com\nHY2_PORT_MODE=single\nHY2_PORT=1234\nREALITY_ADDRESS=203.0.113.1\nREALITY_SNI=example.com\nREALITY_UUID=fixture\nREALITY_PRIVATE_KEY=fixture\nREALITY_PUBLIC_KEY=fixture\nREALITY_SHORT_ID=abcdef\nHY2_ACME_EMAIL=fixture@example.com\nHY2_OBFS_TYPE=mimic\nHY2_PASSWORD=unchanged\n')
            config.write_text('listen: :1234\nauth:\n  type: password\n  password: unchanged\nmimic:\n  enabled: true\nquic:\n  initStreamReceiveWindow: 1234\n')
            self.assertTrue(remove_retired_obfuscation(state,config))
            self.assertIn('HY2_PASSWORD=unchanged',state.read_text());self.assertIn('password: unchanged',config.read_text());self.assertIn('initStreamReceiveWindow: 1234',config.read_text());self.assertNotIn('mimic:',config.read_text())
            self.assertFalse(remove_retired_obfuscation(state,config))
            self.assertTrue(pathlib.Path(str(config)+'.before-fix').exists())
            old_state=pathlib.Path(str(state)+'.before-fix').read_bytes();old_config=pathlib.Path(str(config)+'.before-fix').read_bytes()
            state.write_bytes(old_state);config.write_bytes(old_config)
            def fail_state_write(path,data,mode):
                if pathlib.Path(path)==state and b'HY2_OBFS_TYPE=none' in data: raise OSError('injected migration write failure')
                storage.atomic(path,data,mode)
            with patch('migrate.atomic',side_effect=fail_state_write):
                with self.assertRaises(OSError):remove_retired_obfuscation(state,config)
            self.assertEqual(state.read_bytes(),old_state);self.assertEqual(config.read_bytes(),old_config)

    def test_environment_cannot_execute_commands(self):
        for payload in [b'NODE_NAME=$(reboot)\n',b'NODE_NAME=`id`\n',b'NODE_NAME=x;reboot\n',b'NODE_NAME="$(reboot)"\n']:
            with self.assertRaises(ValueError): storage.environment_values(payload)
        self.assertEqual(storage.environment_values(b'NODE_NAME=literal\\ \\$\\(reboot\\)\n')['NODE_NAME'],'literal $(reboot)')

    def test_incomplete_state_cannot_replace_verified_backup(self):
        with self.assertRaises(ValueError):storage.validate('state.env',b'STACK_MODE=reality_hy2\nMAIN_MODE=reality\n')

    def test_backup_failure_never_rolls_back_healthy_primary(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'panel.env';p.write_bytes(b'KEY=old\n');storage.checkpoint(p,p.read_bytes());p.write_bytes(b'KEY=new\n')
            with patch('storage.checkpoint',side_effect=OSError('backup I/O failure')):
                storage.recover(p)
            self.assertEqual(p.read_bytes(),b'KEY=new\n')

    def test_invalid_yaml_syntax_and_json_shape_recover(self):
        for name,good,bad in [('config.yaml',b'listen: :1234\nauth: {type: password, password: secret}\n',b'listen: [broken\n'),('config.json',b'{"inbounds":[{}]}',b'[]')]:
            with self.subTest(name=name),tempfile.TemporaryDirectory() as folder:
                p=pathlib.Path(folder)/name;p.write_bytes(good);storage.checkpoint(p,good);p.write_bytes(bad);storage.recover(p)
                self.assertEqual(p.read_bytes(),good)

    def test_oversized_file_is_rejected_before_read(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'oversized'
            with p.open('wb') as f:f.truncate(storage.MAX_BYTES+1)
            with self.assertRaises(ValueError):storage.read_bounded(p)

    def test_write_readback_failure_does_not_publish_new_backup(self):
        with tempfile.TemporaryDirectory() as folder:
            p=pathlib.Path(folder)/'panel.env';p.write_bytes(b'KEY=old\n');storage.checkpoint(p,p.read_bytes());candidate=pathlib.Path(folder)/'candidate';candidate.write_bytes(b'KEY=new\n')
            backup=pathlib.Path(str(p)+'.fix-recovery').read_bytes();original=storage.read_bounded
            def read(path,limit=storage.MAX_BYTES):
                if pathlib.Path(path)==p:raise OSError('readback I/O failure')
                return original(path,limit)
            with patch('storage.sys.argv',['storage.py','write',str(p),str(candidate),'600']),patch('storage.read_bounded',read):
                with self.assertRaises(OSError):storage.main()
            self.assertEqual(pathlib.Path(str(p)+'.fix-recovery').read_bytes(),backup)

if __name__=='__main__':unittest.main()
