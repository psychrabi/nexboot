import contextlib, io, json, pathlib, subprocess, tempfile
from unittest.mock import patch
source=pathlib.Path('scripts/anaconda-early.py').read_text().replace('__BASE__','http://server/').replace('__ATTEMPT__','1').replace('__SERIAL__','expected').replace('__SIZE__','4096')
for scenario in ('good','missing','duplicate','local','readonly','wrong-size','wrong-lun'):
    files={'/sys/class/block/sdb/device/vpd_pg80':b'\0\x80\0\x08expected','/sys/class/block/sdb/size':b'8','/sys/class/block/sdb/ro':b'0'}
    if scenario=='missing': files['/sys/class/block/sdb/device/vpd_pg80']=b'xxxxother'
    if scenario=='readonly': files['/sys/class/block/sdb/ro']=b'1'
    if scenario=='wrong-size': files['/sys/class/block/sdb/size']=b'9'
    blocks=['/sys/class/block/sdb']
    if scenario=='duplicate':
        blocks+=['/sys/class/block/sdc'];files.update({k.replace('sdb','sdc'):v for k,v in list(files.items())})
    output=io.StringIO()
    class Keep(io.StringIO):
        def close(self): pass
    output=Keep()
    def fake_open(name, mode='r'):
        if name=='/tmp/nd-partitions.ks':return output
        b=files[name]
        return io.BytesIO(b) if 'b' in mode else io.StringIO(b.decode())
    device='/sys/devices/session1/target1:0:0/1:0:0:0'
    if scenario=='local':device='/sys/devices/pci/ata1/1:0:0:0'
    if scenario=='wrong-lun':device=device[:-1]+'1'
    with patch('builtins.open',fake_open),patch('glob.glob',return_value=blocks),patch('os.path.exists',return_value=False),patch('os.path.realpath',return_value=device),patch('subprocess.check_call') as cmd:
        rc=0
        try:exec(compile(source,'early.py','exec'),{})
        except SystemExit as e:rc=e.code
        reports=[json.loads(c.args[0][c.args[0].index('--data')+1]) for c in cmd.call_args_list if '--data' in c.args[0]]
        if scenario=='good':
            assert rc==0 and reports[-1]['phase']=='installing'
            assert 'ignoredisk --only-use=sdb\n' in output.getvalue()
            assert 'clearpart --all --initlabel --drives=sdb\n' in output.getvalue()
            assert 'sda' not in output.getvalue()
        else:
            assert rc==1 and reports[-1]['phase']=='failed',scenario
            assert output.getvalue()=='',scenario
print('anaconda disk identity scenarios passed')
