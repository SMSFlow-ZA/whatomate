"""Fail-closed image release policy. Reports never print matched secret values."""
import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
import re
import tarfile


def timestamp(value):
    parsed=dt.datetime.fromisoformat(value.replace('Z','+00:00'))
    if parsed.tzinfo is None:raise ValueError('Timezone required')
    return parsed


def fresh(value,now,hours=24):
    age=(now-timestamp(value)).total_seconds()
    if not -300 <= age <= hours*3600:raise ValueError('Stale or future scan evidence')


def evaluate(report,go,policy,image,now):
    if timestamp(policy['expires_utc']) <= now:raise ValueError('Residual review expired')
    if report.get('SchemaVersion')!=2 or report.get('ArtifactType')!='container_image':
        raise ValueError('Unsupported image report')
    fresh(report['CreatedAt'],now)
    meta=report['Metadata']
    if meta.get('ImageID')!=image or meta.get('OS',{}).get('Family')!='debian':
        raise ValueError('Scan candidate identity or OS differs')
    results=report['Results']
    if not results or not any(r.get('Type')=='debian' for r in results) or not any(r.get('Type')=='gobinary' for r in results):
        raise ValueError('Missing OS or application scan')
    configs=[v['config'] for v in go if 'config' in v]
    sboms=[v['SBOM'] for v in go if 'SBOM' in v]
    if len(configs)!=1 or len(sboms)!=1 or not sboms[0].get('go_version') or not sboms[0].get('modules'):
        raise ValueError('Incomplete Go binary scan')
    config=configs[0]
    if config.get('scanner_name')!='govulncheck' or config.get('scanner_version')!='v1.9.0' or config.get('scan_level')!='package':
        raise ValueError('Wrong Go scanner/version/level')
    fresh(config['db_last_modified'],now,72)
    unused={tuple(v) for v in policy['reviewed_unused_go']}
    for entry in go:
        if 'finding' not in entry:continue
        finding=entry['finding'];trace=finding['trace']
        if not trace or any(frame.get('package') or frame.get('function') for frame in trace):
            raise ValueError('Affected Go package or symbol')
        if any((finding['osv'],frame['module'],frame['version']) not in unused for frame in trace):
            raise ValueError('Unreviewed Go module advisory')
    reviewed={tuple(v) for v in policy['reviewed_high']}
    residual=0
    for result in results:
        if result.get('Secrets'):raise ValueError('Embedded secret detected; see restricted report')
        for vuln in result.get('Vulnerabilities') or []:
            severity=vuln['Severity']
            if severity not in ('UNKNOWN','LOW','MEDIUM','HIGH','CRITICAL'):raise ValueError('Unknown severity schema')
            if severity=='CRITICAL':raise ValueError('Critical image vulnerability')
            identity=(result['Type'],vuln['PkgName'],vuln['InstalledVersion'],vuln['VulnerabilityID'])
            if severity=='HIGH':
                if vuln.get('FixedVersion') or identity not in reviewed:
                    raise ValueError('Fixable or unreviewed high image vulnerability')
                residual+=1
            elif severity=='UNKNOWN':
                if ((result['Type']!='gobinary' or (vuln['VulnerabilityID'],vuln['PkgName'],vuln['InstalledVersion']) not in unused)
                        and identity not in {tuple(v) for v in policy.get('reviewed_unknown',[])}):
                    raise ValueError('Unreviewed unknown-severity vulnerability')
    return {'reviewed_unfixed_high_entries':residual,'affected_go_packages':0,'embedded_secrets':0}


def stream_json(path):
    remaining=path.read_text(encoding='utf-8');decoder=json.JSONDecoder();values=[]
    while remaining.strip():
        remaining=remaining.lstrip();value,end=decoder.raw_decode(remaining);values.append(value);remaining=remaining[end:]
    return values


def sha(path):
    with path.open('rb') as stream:return hashlib.file_digest(stream,'sha256').hexdigest()


def archive_config(path):
    with tarfile.open(path) as archive:
        manifests=json.load(archive.extractfile('manifest.json'))
        if len(manifests)!=1:raise ValueError('Exactly one saved image required')
        config=archive.extractfile(manifests[0]['Config']).read()
        return 'sha256:'+hashlib.sha256(config).hexdigest(),json.loads(config)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode',choices=['approve','verify'])
    parser.add_argument('--evidence',type=Path,required=True)
    parser.add_argument('--source',required=True)
    parser.add_argument('--local-id',required=True)
    args=parser.parse_args();folder=args.evidence
    if not re.fullmatch('[a-f0-9]{40}',args.source) or not re.fullmatch('sha256:[a-f0-9]{64}',args.local_id):
        raise ValueError('Invalid candidate identity')
    names=['whatomate-image.tar','whatomate-binary','trivy.json','govulncheck.json','trivy-db.json','trivy-version.json']
    hashes={name:sha(folder/name) for name in names}
    now=dt.datetime.now(dt.timezone.utc)
    if args.mode=='verify':
        approval=json.loads((folder/'security-approval.json').read_text())
        fresh(approval['checked_utc'],now)
        if approval['hashes']!=hashes or approval['source']!=args.source or approval['local_id']!=args.local_id:
            raise ValueError('Candidate changed after security approval')
        print('Scanned candidate unchanged');return
    image,config=archive_config(folder/'whatomate-image.tar')
    if config.get('architecture')!='amd64' or config.get('os')!='linux' or config['config']['Labels'].get('org.opencontainers.image.revision')!=args.source:
        raise ValueError('Wrong candidate platform/source')
    version=json.loads((folder/'trivy-version.json').read_text())
    if version.get('Version')!='0.76.1':raise ValueError('Wrong Trivy version')
    fresh(json.loads((folder/'trivy-db.json').read_text())['UpdatedAt'],now,72)
    policy=json.loads(Path(__file__).with_name('release-security-policy.json').read_text())
    result=evaluate(json.loads((folder/'trivy.json').read_text()),stream_json(folder/'govulncheck.json'),policy,image,now)
    approval={**result,'checked_utc':now.isoformat(),'source':args.source,'local_id':args.local_id,'config_id':image,'hashes':hashes}
    (folder/'security-approval.json').write_text(json.dumps(approval,indent=2)+'\n')
    print(json.dumps(result))


if __name__=='__main__':main()
