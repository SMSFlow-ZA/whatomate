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


def safe_report(report):
    # Raw scanner output may contain actual secrets and image environment values.
    # Retain reviewable advisory identities and counts, never matched content.
    return {'SchemaVersion':report.get('SchemaVersion'),'CreatedAt':report.get('CreatedAt'),
            'ImageID':report.get('Metadata',{}).get('ImageID'),
            'Results':[{'Type':result.get('Type'),'Class':result.get('Class'),
                        'SecretCount':len(result.get('Secrets') or []),
                        'Vulnerabilities':[{k:v.get(k) for k in ('VulnerabilityID','PkgName',
                            'InstalledVersion','FixedVersion','Severity','Status')} for v in result.get('Vulnerabilities') or []]}
                       for result in report.get('Results') or []]}


def bind_candidate(config,local,identity):
    # Docker's containerd store can identify an image by its manifest/index,
    # rather than the config digest. Bind immutable rootfs and runtime settings
    # from inspection of that exact local ID to the saved archive before push.
    def present(values):return {k:v for k,v in values.items() if v is not None}
    if (local.get('Id')!=identity or local.get('Architecture')!=config.get('architecture')
            or local.get('Os')!=config.get('os')
            or present(local['Config'])!=present(config['config'])
            or local['RootFS']['Type']!=config['rootfs']['type']
            or local['RootFS']['Layers']!=config['rootfs']['diff_ids']):
        raise ValueError('Saved scan candidate differs from local publish image')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode',choices=['approve','verify','redact'])
    parser.add_argument('--evidence',type=Path,required=True)
    parser.add_argument('--source')
    parser.add_argument('--local-id')
    args=parser.parse_args();folder=args.evidence
    if args.mode=='redact':
        (folder/'trivy-safe.json').write_text(json.dumps(safe_report(json.loads((folder/'trivy.json').read_text())),indent=2)+'\n')
        return
    if not re.fullmatch('[a-f0-9]{40}',args.source) or not re.fullmatch('sha256:[a-f0-9]{64}',args.local_id):
        raise ValueError('Invalid candidate identity')
    names=['whatomate-image.tar','whatomate-binary','trivy.json','govulncheck.json','trivy-db.json','trivy-version.json','local-image.json']
    hashes={name:sha(folder/name) for name in names}
    now=dt.datetime.now(dt.timezone.utc)
    if args.mode=='verify':
        approval=json.loads((folder/'security-approval.json').read_text())
        fresh(approval['checked_utc'],now)
        if approval['hashes']!=hashes or approval['source']!=args.source or approval['local_id']!=args.local_id:
            raise ValueError('Candidate changed after security approval')
        print('Scanned candidate unchanged');return
    image,config=archive_config(folder/'whatomate-image.tar')
    local=json.loads((folder/'local-image.json').read_text())
    if local.get('Id')!=args.local_id:raise ValueError('Frozen local image identity differs')
    # Classic Docker IDs are the raw config digest: equality binds every config
    # byte and layer cryptographically, without comparing its legacy inspection
    # defaults with OCI fields. Containerd index/manifest IDs need the detailed
    # runtime/rootfs comparison instead. Never weaken that path to a tag match.
    if image!=args.local_id:bind_candidate(config,local,args.local_id)
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
