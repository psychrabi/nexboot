from __future__ import print_function
import os,sys,tempfile,shutil,types
root=tempfile.mkdtemp()
try:
    ns={}
    patch=os.path.join('scripts','anaconda-el7-iscsi.py')
    if os.path.exists(patch):
        exec(compile(open(patch).read().split('# Load the original')[0],patch,'exec'),ns)
        ns['_nd_sys'] = root
    vendor=open('testdata/el7-startibft.py').read()
    actual=os.environ.get('ND_TEST_BLIVET_SOURCE')
    if actual:
        full=open(actual).read();start=full.index('    def _startIBFT');end=full.index('    def stabilize',start)
        assert vendor.split('class Vendor(object):\n')[1] == full[start:end], 'fixture differs from actual DVD'
        vendor='class Vendor(object):\n'+full[start:end]
    if '_nd_patch' in ns: vendor=ns['_nd_patch'](vendor)
    class Log(object):
        def info(self,*a):pass
        def error(self,*a):pass
    class Node(object):
        name='iqn.test'; address='192.0.2.1';port=3260;iface='default'
        error='initiator reported error (15 - session exists)'
        def login(self):
            if self.error:raise IOError(self.error)
    node=Node()
    ns.update(flags=type('Flags',(),{'ibft':True})(),libiscsi=type('Lib',(),{'discover_firmware':staticmethod(lambda:[node])})(),log=Log())
    exec(compile(vendor,'actual-DVD-startIBFT','exec'),ns)
    def put(path,value):
        p=os.path.join(root,path)
        if not os.path.isdir(os.path.dirname(p)):os.makedirs(os.path.dirname(p))
        with open(p,'w') as f:f.write(value)
    values={'class/iscsi_session/session1/targetname':node.name,'class/iscsi_session/session1/ifacename':'default','class/iscsi_session/session1/initiatorname':'iqn.initiator','class/iscsi_session/session1/state':'LOGGED_IN','class/iscsi_connection/connection1:0/address':node.address,'class/iscsi_connection/connection1:0/port':'3260'}
    for k,v in values.items():put(k,v)
    def run(expected):
        v=ns['Vendor']();v.ibftNodes=[];v._initiator='iqn.initiator';v.stabilize=lambda:None;v._startIBFT();assert bool(v.ibftNodes)==expected, (node.error,v.ibftNodes)
    run(True)
    node.error=None;run(True)
    for error in ['initiator reported error (24 - authentication failed)','15 - session exists','initiator reported error (150 - session exists)']:
        node.error=error;run(False)
    node.error='initiator reported error (15 - session exists)'
    for k,v in values.items():
        put(k,'mismatch');run(False);put(k,v)
        os.unlink(os.path.join(root,k));run(False);put(k,v)
    shutil.copytree(os.path.join(root,'class/iscsi_session/session1'),os.path.join(root,'class/iscsi_session/session2'))
    shutil.copytree(os.path.join(root,'class/iscsi_connection/connection1:0'),os.path.join(root,'class/iscsi_connection/connection2:0'))
    run(False)
    if '_nd_patch' in ns:
        for bad in ['no vendor marker',open('testdata/el7-startibft.py').read()*2]:
            try:ns['_nd_patch'](bad)
            except RuntimeError:pass
            else:raise AssertionError('unsupported vendor source accepted')
    print('actual DVD _startIBFT: success, error15, mismatch, missing, duplicate, other error PASS')
finally:shutil.rmtree(root)
