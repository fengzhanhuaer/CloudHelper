"""PC-side checks using the existing ASUS session. Never persist credentials."""
import base64
import http.client
from http.cookies import SimpleCookie
import json
import ssl
from urllib.parse import urlencode

def settings_credentials(path):
    blob=path.read_bytes()
    if blob[:4]!=b'HDR2':raise ValueError('Expected original ASUS HDR2 settings export')
    size=int.from_bytes(blob[4:7],'little');salt=blob[7]
    decoded=bytes(0 if value>=0xfd else (0xff+salt-value)&0xff for value in blob[8:8+size])
    values=dict(item.split(b'=',1) for item in decoded.split(b'\0') if b'=' in item)
    return values[b'http_username'].decode(),values[b'http_passwd'].decode()

class StockWeb:
    def __init__(self,remote,credentials=None):
        self.address=remote('nvram get lan_ipaddr').decode().strip()
        self.tls=remote('nvram get http_enable').strip()==b'1'
        key='https_lanport' if self.tls else 'http_lanport'
        self.port=int(remote('nvram get '+key).strip() or (443 if self.tls else 80))
        self.origin=('https://' if self.tls else 'http://')+self.address
        if self.port!=(443 if self.tls else 80):self.origin+=':'+str(self.port)
        self.cookie=''
        # The authorized SSH channel supplies credentials directly to memory.
        if credentials:username,password=credentials
        else:
            username=remote('nvram get http_username').decode().strip()
            password=remote('nvram get http_passwd').decode().strip()
        if not username or not password:raise RuntimeError('Original ASUS credentials required for verification')
        authorization=base64.b64encode((username+':'+password).encode()).decode()
        code,headers,body=self.request('/login.cgi',urlencode({'login_authorization':authorization,'next_page':'index.asp'}).encode(),content_type='application/x-www-form-urlencoded')
        cookies=SimpleCookie()
        for name,value in headers:
            if name.lower()=='set-cookie':cookies.load(value)
        if 'asus_token' not in cookies:raise RuntimeError('Original ASUS web login failed; credentials were not recorded')
        self.cookie='asus_token='+cookies['asus_token'].value

    def request(self,path,body=None,authenticated=True,content_type='application/json'):
        factory=http.client.HTTPSConnection if self.tls else http.client.HTTPConnection
        options={'timeout':12}
        if self.tls:options['context']=ssl._create_unverified_context()
        client=factory(self.address,self.port,**options)
        try:
            headers={'Referer':self.origin+'/index.asp','Origin':self.origin}
            if authenticated and self.cookie:headers['Cookie']=self.cookie
            if body is not None:headers['Content-Type']=content_type
            client.request('POST' if body is not None else 'GET',path,body=body,headers=headers)
            response=client.getresponse()
            return response.status,response.getheaders(),response.read()
        finally:client.close()

    def api(self,request):
        code,headers,body=self.request('/user/cloudhelper-router/api.asp',json.dumps(request).encode())
        if code!=200:raise RuntimeError('ASUS management request failed')
        return json.loads(body)
