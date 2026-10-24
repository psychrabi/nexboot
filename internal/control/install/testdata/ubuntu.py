import io, json, pathlib, subprocess, urllib.request, urllib.error
from unittest.mock import patch, MagicMock
source=pathlib.Path('scripts/iso-startup.py').read_text()

def run(phase, error_once=False, identity_error=False):
    calls=[]
    responses=0
    def urlopen(request, **kw):
        nonlocal responses
        responses+=1
        if error_once and responses==1: raise OSError('network not ready')
        if isinstance(request,str): return io.BytesIO(json.dumps({'phase':phase,'attempt':1,'boot_id':'this-boot'}).encode())
        if phase=='initializing':
            path.return_value.write_text.assert_called_with('')
        calls.append(json.loads(request.data))
        return io.BytesIO(b'{}')
    with patch('urllib.request.urlopen',urlopen), patch('subprocess.run') as command, patch('pathlib.Path') as path, patch('shutil.which',return_value=None), patch('time.sleep'):
        path.return_value.glob.return_value=[MagicMock()]
        if identity_error: path.return_value.write_text.side_effect=OSError('cannot clear template identity')
        exec(source.replace('__BASE__','http://server/install/token/'),{})
        if identity_error:
            assert calls==[], 'must not announce installed before clearing shared identities'
            command.assert_not_called()
        elif phase=='initializing':
            assert calls[0]['phase']=='installed'
            path.return_value.write_text.assert_called_once_with('')
            command.assert_called_once_with(['systemctl','poweroff'],check=True)
        elif phase=='verifying':
            assert calls==[{'attempt':1,'phase':'verified','boot_id':'this-boot','message':'Ubuntu 普通无盘启动回报成功'}]
            command.assert_not_called()
        else:
            assert calls==[]
            command.assert_not_called()
run('initializing',True)
run('initializing',identity_error=True)
run('verifying')
run('ready')
