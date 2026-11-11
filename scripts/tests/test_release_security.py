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
        self.metadata_modified=self.now.isoformat()
        self.image='sha256:'+'1'*64
        self.report={'SchemaVersion':2,'CreatedAt':self.now.isoformat(),'ArtifactType':'container_image',
            'Metadata':{'ImageID':self.image,'OS':{'Family':'debian','Name':'13.7'}},
            'Results':[{'Target':'debian','Class':'os-pkgs','Type':'debian','Vulnerabilities':[]},
                       {'Target':'app/whatomate','Class':'lang-pkgs','Type':'gobinary','Vulnerabilities':[]}]}
        self.go=[{'config':{'scanner_name':'govulncheck','scanner_version':'v1.9.0',
                          'scan_level':'package','db':'https://vuln.go.dev','db_last_modified':self.now.isoformat()}},
                 {'SBOM':{'go_version':'go1.26.10','modules':[{'path':'stdlib','version':'v1.26.10'}]}}]
        self.policy={'expires_utc':(self.now+dt.timedelta(days=1)).isoformat(),'reviewed_high':[],
                     'reviewed_unused_go':[]}

    def database(self,modified=None):return {'url':'https://vuln.go.dev/index/db.json','checked_utc':self.now.isoformat(),'modified':modified or self.metadata_modified}

    def check(self):return security.evaluate(self.report,self.go,self.policy,self.image,self.now,self.database())

    def vuln(self,**changes):
        value={'VulnerabilityID':'CVE-test','PkgName':'codec','InstalledVersion':'1.2','Severity':'HIGH'}
        value.update(changes);self.report['Results'][0]['Vulnerabilities']=[value];return value

    def test_openssl_backport_requires_verified_fingerprint_and_exact_finding(self):
        finding = self.vuln(Severity='UNKNOWN', PkgName='libssl3t64',
                            InstalledVersion=security.OPENSSL_BACKPORT_VERSION,
                            VulnerabilityID='CVE-2026-95691')
        def evaluate(fingerprint):
            return security.evaluate(self.report, self.go, self.policy, self.image,
                                     self.now, self.database(), fingerprint)

        for fingerprint in (None, 'unverified'):
            with self.assertRaisesRegex(ValueError, 'Unreviewed'):
                evaluate(fingerprint)
        verified = security.OPENSSL_BACKPORT_FINGERPRINT
        self.assertEqual(evaluate(verified)['verified_openssl_backport_entries'], 1)
        original = finding.copy()
        for changes in ({'InstalledVersion': '3.5.7-1~deb13u3'},
                        {'VulnerabilityID': 'CVE-2026-99999'},
                        {'FixedVersion': '3.5.7-1~deb13u4'},
                        {'Severity': 'CRITICAL'}):
            with self.subTest(changes=changes):
                finding.update(original)
                finding.pop('FixedVersion', None)
                finding.update(changes)
                with self.assertRaises(ValueError):
                    evaluate(verified)

    def test_clean_candidate_passes(self):self.check()

    def test_retained_report_never_contains_secret_matches_or_image_environment(self):
        self.report['Metadata']['ImageConfig']={'config':{'Env':['PRIVATE=secret-sentinel']}}
        self.report['Results'][0]['Secrets']=[{'RuleID':'private-key','Match':'secret-sentinel',
            'Code':{'Lines':[{'Content':'secret-sentinel'}]}}]
        value=security.safe_report(self.report)
        self.assertNotIn('secret-sentinel',json.dumps(value))
        self.assertEqual(value['Results'][0]['SecretCount'],1)

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

    def test_reviewed_unused_go_cve_alias_needs_matching_official_finding(self):
        self.go.extend([{'osv':{'id':'GO-test','aliases':['CVE-test']}},
                        {'finding':{'osv':'GO-test','trace':[{'module':'example.test/unused','version':'v1'}]}}])
        self.policy['reviewed_unused_go']=[['GO-test','example.test/unused','v1']]
        self.report['Results'][1]['Vulnerabilities']=[{'VulnerabilityID':'CVE-test',
            'PkgName':'example.test/unused','InstalledVersion':'v1','FixedVersion':'v2','Severity':'UNKNOWN'}]
        self.check()
        self.go[0]['config']['db']='https://untrusted.example.invalid'
        with self.assertRaises(ValueError):self.check()
        self.go[0]['config']['db']='https://vuln.go.dev'
        self.report['Results'][1]['Vulnerabilities'][0]['InstalledVersion']='v2'
        with self.assertRaises(ValueError):self.check()
        self.report['Results'][1]['Vulnerabilities'][0]['InstalledVersion']='v1'
        self.go[-1]['finding']['trace'][0]['package']='example.test/unused'
        with self.assertRaises(ValueError):self.check()
        del self.go[-1]['finding']['trace'][0]['package']
        self.go[2]['osv']['aliases']=[]
        with self.assertRaises(ValueError):self.check()
        self.go[2]['osv']['aliases']=['CVE-test'];self.go.pop()
        with self.assertRaises(ValueError):self.check()

    def test_approval_detects_changed_binary_archive_source_and_image(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as directory:
            folder=Path(directory)
            config=json.dumps({'architecture':'amd64','os':'linux','config':{'Labels':{
                'org.opencontainers.image.revision':'a'*40}},
                'rootfs':{'type':'layers','diff_ids':['sha256:'+'3'*64]}}).encode()
            with tarfile.open(folder/'whatomate-image.tar','w') as archive:
                for name,value in [('config.json',config),('manifest.json',b'[{"Config":"config.json"}]')]:
                    info=tarfile.TarInfo(name);info.size=len(value);archive.addfile(info,io.BytesIO(value))
            self.report['Metadata']['ImageID']='sha256:'+security.hashlib.sha256(config).hexdigest()
            (folder/'whatomate-binary').write_bytes(b'fixture binary')
            (folder/'trivy.json').write_text(json.dumps(self.report))
            (folder/'govulncheck.json').write_text('\n'.join(json.dumps(v) for v in self.go))
            (folder/'govulndb.json').write_text(json.dumps(self.database()))
            (folder/'trivy-db.json').write_text(json.dumps({'UpdatedAt':self.now.isoformat()}))
            (folder/'trivy-version.json').write_text('{"Version":"0.76.1"}')
            local={'Id':self.image,'Architecture':'amd64','Os':'linux','Config':json.loads(config)['config'],
                   'RootFS':{'Type':'layers','Layers':['sha256:'+'3'*64]}}
            (folder/'local-image.json').write_text(json.dumps(local))
            args=['tool','approve','--evidence',directory,'--source','a'*40,'--local-id',self.image]
            with patch('sys.argv',args):security.main()
            args[1]='verify'
            with patch('sys.argv',args):security.main()
            # Docker 28 overlay2 returns a config digest and adds legacy
            # container defaults that are absent from the OCI archive config.
            local['Id']=self.report['Metadata']['ImageID']
            local['Config'].update(Hostname='',Domainname='',AttachStdin=False,
                                   AttachStdout=False,AttachStderr=False,Tty=False,
                                   OpenStdin=False,StdinOnce=False,Image='')
            (folder/'local-image.json').write_text(json.dumps(local))
            args[-1]=local['Id'];args[1]='approve'
            with patch('sys.argv',args):security.main()
            args[1]='verify'
            with patch('sys.argv',args):security.main()
            for name in ['whatomate-binary','whatomate-image.tar','govulncheck.json','govulndb.json']:
                path=folder/name;old=path.read_bytes();path.write_bytes(old+b'changed')
                with patch('sys.argv',args),self.assertRaisesRegex(ValueError,'changed'):security.main()
                path.write_bytes(old)
            args[-1]='sha256:'+'2'*64
            with patch('sys.argv',args),self.assertRaisesRegex(ValueError,'changed'):security.main()

    def test_unchanged_official_database_is_current_when_freshly_retrieved(self):
        old=(self.now-dt.timedelta(days=14)).isoformat()
        self.go[0]['config']['db_last_modified']=old
        self.metadata_modified=old
        security.validate_go_database(self.go[0]['config'],self.database(),self.now)
        self.check()

    def test_missing_stale_future_or_wrong_official_database_proof_blocks(self):
        original=self.database()
        for proof in (None,{}, {**original,'checked_utc':(self.now-dt.timedelta(hours=25)).isoformat()},
                      {**original,'checked_utc':(self.now+dt.timedelta(minutes=10)).isoformat()},
                      {**original,'url':'https://untrusted.invalid/index/db.json'},
                      {**original,'modified':(self.now-dt.timedelta(seconds=1)).isoformat()}):
            with self.subTest(proof=proof),self.assertRaises((ValueError,KeyError,TypeError)):
                security.validate_go_database(self.go[0]['config'],proof,self.now)
        wrong={**self.go[0]['config'],'db':'https://untrusted.invalid'}
        with self.assertRaises(ValueError):security.validate_go_database(wrong,original,self.now)
        future=(self.now+dt.timedelta(minutes=10)).isoformat()
        with self.assertRaises(ValueError):security.validate_go_database({**self.go[0]['config'],'db_last_modified':future},self.database(future),self.now)

    def test_database_collector_pins_endpoint_and_rejects_redirect_or_oversize(self):
        class Response(io.BytesIO):
            def geturl(self):return self.url
        def response(body,url='https://vuln.go.dev/index/db.json'):
            result=Response(body);result.url=url;return result
        body=json.dumps({'modified':self.go[0]['config']['db_last_modified']}).encode()
        with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as directory:
            folder=Path(directory)
            with patch.object(security.urllib.request,'urlopen',return_value=response(body)) as request:
                security.collect_go_database(folder)
                self.assertEqual(request.call_args.args[0].full_url,'https://vuln.go.dev/index/db.json')
            proof=json.loads((folder/'govulndb.json').read_text())
            security.validate_go_database(self.go[0]['config'],proof,dt.datetime.now(dt.timezone.utc))
            for value in (response(body,'https://other.invalid'),response(b'x'*4097),response(b'{}')):
                with patch.object(security.urllib.request,'urlopen',return_value=value),self.assertRaises((ValueError,KeyError)):
                    security.collect_go_database(folder)

    def test_candidate_evidence_is_bound_before_approval(self):
        config={'architecture':'amd64','os':'linux','config':{'Env':['PUBLIC=one']},
                'rootfs':{'type':'layers','diff_ids':['sha256:'+'3'*64]}}
        local={'Id':self.image,'Architecture':'amd64','Os':'linux','Config':{'Env':['PUBLIC=one']},
               'RootFS':{'Type':'layers','Layers':['sha256:'+'3'*64]}}
        security.bind_candidate(config,local,self.image)
        for field,value in [('Id','sha256:'+'2'*64),('Architecture','arm64'),
                            ('Config',{'Env':['PUBLIC=different']}),('RootFS',{'Type':'layers','Layers':[]})]:
            changed={**local,field:value}
            with self.subTest(field=field),self.assertRaises(ValueError):
                security.bind_candidate(config,changed,self.image)

if __name__=='__main__':unittest.main()
