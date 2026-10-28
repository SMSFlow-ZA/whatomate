import copy
import datetime as dt
import importlib.util
from pathlib import Path
import unittest
import json
import io
import tarfile
import tempfile
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('security',Path(__file__).parents[1]/'release_security.py')
security=importlib.util.module_from_spec(spec);spec.loader.exec_module(security)

class SecurityTests(unittest.TestCase):
    def setUp(self):
        self.now=dt.datetime.now(dt.timezone.utc)
        self.image='sha256:'+'1'*64
        self.report={'SchemaVersion':2,'CreatedAt':self.now.isoformat(),'ArtifactType':'container_image',
            'Metadata':{'ImageID':self.image,'OS':{'Family':'debian','Name':'13.7'}},
            'Results':[{'Target':'debian','Class':'os-pkgs','Type':'debian','Vulnerabilities':[]},
                       {'Target':'app/whatomate','Class':'lang-pkgs','Type':'gobinary','Vulnerabilities':[]}]}
        self.go=[{'config':{'scanner_name':'govulncheck','scanner_version':'v1.9.0',
                          'scan_level':'package','db_last_modified':self.now.isoformat()}},
                 {'SBOM':{'go_version':'go1.26.10','modules':[{'path':'stdlib','version':'v1.26.10'}]}}]
        self.policy={'expires_utc':(self.now+dt.timedelta(days=1)).isoformat(),'reviewed_high':[],
                     'reviewed_unused_go':[]}

    def check(self):return security.evaluate(self.report,self.go,self.policy,self.image,self.now)

    def vuln(self,**changes):
        value={'VulnerabilityID':'CVE-test','PkgName':'codec','InstalledVersion':'1.2','Severity':'HIGH'}
        value.update(changes);self.report['Results'][0]['Vulnerabilities']=[value];return value

    def test_clean_candidate_passes(self):self.check()

    def test_critical_new_high_fixable_high_and_secrets_block(self):
        for change in ({},{'Severity':'CRITICAL'},{'FixedVersion':'1.3'}):
            with self.subTest(change=change):
                self.vuln(**change)
                with self.assertRaises(ValueError):self.check()
        self.report['Results'][0]['Vulnerabilities']=[]
        self.report['Results'][0]['Secrets']=[{'RuleID':'private-key','Match':'DO NOT PRINT'}]
        with self.assertRaisesRegex(ValueError,'secret'):self.check()

    def test_reviewed_unfixed_high_is_exact_and_expires(self):
        self.vuln();self.policy['reviewed_high']=[['debian','codec','1.2','CVE-test']];self.check()
        self.vuln(InstalledVersion='1.3')
        with self.assertRaises(ValueError):self.check()
        self.vuln(FixedVersion='1.4')
        with self.assertRaises(ValueError):self.check()
        self.vuln();self.policy['expires_utc']=(self.now-dt.timedelta(seconds=1)).isoformat()
        with self.assertRaises(ValueError):self.check()

    def test_missing_stale_malformed_or_substituted_scan_blocks(self):
        original=copy.deepcopy(self.report)
        for change in ({'Results':[]},{'SchemaVersion':1},{'Metadata':{}},
                       {'CreatedAt':(self.now-dt.timedelta(days=2)).isoformat()}):
            self.report={**original,**change}
            with self.assertRaises(ValueError):self.check()
        self.report=original;self.report['Metadata']['ImageID']='sha256:'+'2'*64
        with self.assertRaises(ValueError):self.check()

    def test_go_imported_package_blocks_even_without_call(self):
        self.go.append({'finding':{'osv':'GO-test','trace':[{'module':'example.test/unused','package':'example.test/unused'}]}})
        with self.assertRaises(ValueError):self.check()

    def test_only_reviewed_unused_module_is_allowed(self):
        self.go.append({'finding':{'osv':'GO-test','trace':[{'module':'example.test/unused','version':'v1'}]}})
        with self.assertRaises(ValueError):self.check()
        self.policy['reviewed_unused_go']=[['GO-test','example.test/unused','v1']];self.check()
        self.go[-1]['finding']['trace'][0]['package']='example.test/unused'
        with self.assertRaises(ValueError):self.check()

    def test_empty_incomplete_and_wrong_go_scan_blocks(self):
        for change in ([],self.go[:1],[{},{}]):
            old=self.go;self.go=change
            with self.assertRaises(ValueError):self.check()
            self.go=old
        self.go[0]['config']['scan_level']='module'
        with self.assertRaises(ValueError):self.check()

    def test_approval_detects_changed_binary_archive_source_and_image(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as directory:
            folder=Path(directory)
            config=json.dumps({'architecture':'amd64','os':'linux','config':{'Labels':{
                'org.opencontainers.image.revision':'a'*40}}}).encode()
            with tarfile.open(folder/'whatomate-image.tar','w') as archive:
                for name,value in [('config.json',config),('manifest.json',b'[{"Config":"config.json"}]')]:
                    info=tarfile.TarInfo(name);info.size=len(value);archive.addfile(info,io.BytesIO(value))
            self.report['Metadata']['ImageID']='sha256:'+security.hashlib.sha256(config).hexdigest()
            (folder/'whatomate-binary').write_bytes(b'fixture binary')
            (folder/'trivy.json').write_text(json.dumps(self.report))
            (folder/'govulncheck.json').write_text('\n'.join(json.dumps(v) for v in self.go))
            (folder/'trivy-db.json').write_text(json.dumps({'UpdatedAt':self.now.isoformat()}))
            (folder/'trivy-version.json').write_text('{"Version":"0.76.1"}')
            args=['tool','approve','--evidence',directory,'--source','a'*40,'--local-id',self.image]
            with patch('sys.argv',args):security.main()
            args[1]='verify'
            with patch('sys.argv',args):security.main()
            for name in ['whatomate-binary','whatomate-image.tar','govulncheck.json']:
                path=folder/name;old=path.read_bytes();path.write_bytes(old+b'changed')
                with patch('sys.argv',args),self.assertRaisesRegex(ValueError,'changed'):security.main()
                path.write_bytes(old)
            args[-1]='sha256:'+'2'*64
            with patch('sys.argv',args),self.assertRaisesRegex(ValueError,'changed'):security.main()

if __name__=='__main__':unittest.main()
