// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

(function(){
  document.body.classList.remove('no-js');
  /* reduced motion slows the grid instead of freezing it; CSS drops the slide offsets */
  var pace = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? .35 : 1;
  var yr=document.getElementById('yr'); if(yr) yr.textContent=new Date().getFullYear();

  /* reveal on scroll */
  var rv=document.querySelectorAll('.rv');
  if(!('IntersectionObserver' in window)){ rv.forEach(function(e){e.classList.add('in')}); }
  else{
    var io=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){e.target.classList.add('in');io.unobserve(e.target)}})},{rootMargin:'0px 0px -8% 0px',threshold:.08});
    rv.forEach(function(e){io.observe(e)});
  }

  /* tabs */
  var tabs=[].slice.call(document.querySelectorAll('[role=tab]'));
  function sel(t){
    tabs.forEach(function(b){var on=b===t;b.setAttribute('aria-selected',on);b.tabIndex=on?0:-1;document.getElementById(b.getAttribute('aria-controls')).hidden=!on});
  }
  var win=document.getElementById('tb-w');
  if(win&&/win/i.test((navigator.userAgentData&&navigator.userAgentData.platform)||navigator.platform||'')) sel(win);
  tabs.forEach(function(t,i){
    t.addEventListener('click',function(){sel(t)});
    t.addEventListener('keydown',function(e){
      var d=e.key==='ArrowRight'?1:e.key==='ArrowLeft'?-1:0; if(!d) return;
      var n=tabs[(i+d+tabs.length)%tabs.length]; sel(n); n.focus(); e.preventDefault();
    });
  });

  /* copy */
  document.querySelectorAll('[data-copy]').forEach(function(b){
    b.addEventListener('click',function(){
      var txt=b.getAttribute('data-copy');
      var done=function(){b.textContent='Copied';setTimeout(function(){b.textContent='Copy'},1600)};
      if(navigator.clipboard) navigator.clipboard.writeText(txt).then(done,function(){});
    });
  });

  /* Expand proof screenshots at their original resolution, with scrollable zoom. */
  var viewer=document.getElementById('proof-viewer');
  if(viewer && typeof viewer.showModal==='function'){
    var viewerImage=document.getElementById('proof-viewer-image');
    var zoomLevel=document.getElementById('proof-zoom-level');
    var zoomOut=document.getElementById('proof-zoom-out');
    var zoomIn=document.getElementById('proof-zoom-in');
    var zoom=100, imageWidth=1280;
    function setZoom(value){
      zoom=Math.max(50,Math.min(300,value));
      viewerImage.style.width=(imageWidth*zoom/100)+'px';
      zoomLevel.textContent=zoom+'%';
      zoomOut.disabled=zoom===50; zoomIn.disabled=zoom===300;
    }
    document.querySelectorAll('[data-proof-image]').forEach(function(link){
      link.addEventListener('click',function(event){
        event.preventDefault();
        var image=link.querySelector('img');
        imageWidth=image.naturalWidth || 1280;
        viewerImage.src=link.href; viewerImage.alt=image.alt;
        setZoom(100); viewer.showModal();
        viewerImage.parentElement.scrollTop=0; viewerImage.parentElement.scrollLeft=0;
      });
    });
    zoomOut.addEventListener('click',function(){setZoom(zoom-25)});
    zoomIn.addEventListener('click',function(){setZoom(zoom+25)});
    document.getElementById('proof-viewer-close').addEventListener('click',function(){viewer.close()});
  }

  /* replay trace when it scrolls back into view */
  var trace=document.getElementById('trace');
  if(trace && 'IntersectionObserver' in window){
    var seen=false;
    new IntersectionObserver(function(es){es.forEach(function(e){
      if(e.isIntersecting && seen){ var h=trace.innerHTML; trace.innerHTML=''; void trace.offsetWidth; trace.innerHTML=h; }
      if(e.isIntersecting) seen=true;
    })},{threshold:.4}).observe(trace);
  }

  /* perspective grid + packets */
  var c=document.getElementById('grid'); if(!c||!c.getContext) return;
  var x=c.getContext('2d'), W=0,H=0,dpr=1,t0=performance.now(),packets=[],frame=0;
  function size(){dpr=Math.min(window.devicePixelRatio||1,2);W=c.clientWidth;H=c.clientHeight;c.width=W*dpr;c.height=H*dpr;x.setTransform(dpr,0,0,dpr,0,0)}
  size(); window.addEventListener('resize',size);
  for(var i=0;i<26;i++) packets.push({lane:(Math.random()*2-1),z:Math.random(),s:.0012+Math.random()*.0028,c:Math.random()<.6?'0,240,255':(Math.random()<.5?'255,43,214':'182,255,59')});
  function schedule(){ if(!frame && !document.hidden) frame=requestAnimationFrame(draw); }
  function draw(now){
    frame=0;
    var t=(now-t0)/1000*pace;
    x.clearRect(0,0,W,H);
    var hy=H*.5, vx=W*.5;
    /* horizon glow */
    var g=x.createLinearGradient(0,hy-120,0,hy+40); g.addColorStop(0,'rgba(255,43,214,0)'); g.addColorStop(.75,'rgba(255,43,214,.10)'); g.addColorStop(1,'rgba(0,240,255,0)');
    x.fillStyle=g; x.fillRect(0,hy-120,W,160);
    /* sun-ish disk */
    var sg=x.createRadialGradient(vx,hy,0,vx,hy,W*.35); sg.addColorStop(0,'rgba(0,240,255,.08)'); sg.addColorStop(1,'rgba(0,240,255,0)'); x.fillStyle=sg; x.fillRect(0,0,W,H);
    x.lineWidth=1;
    /* verticals */
    var n=28;
    for(var i=-n;i<=n;i++){
      var bx=vx+i*(W/n)*1.6;
      x.strokeStyle='rgba(0,240,255,'+(0.10-Math.abs(i)/n*0.07)+')';
      x.beginPath(); x.moveTo(vx+i*6,hy); x.lineTo(bx,H); x.stroke();
    }
    /* horizontals scrolling */
    var off=(t*.35)%1;
    for(var j=0;j<22;j++){
      var p=(j+off)/22, y=hy+Math.pow(p,2.4)*(H-hy);
      x.strokeStyle='rgba(0,240,255,'+(0.02+p*0.16)+')';
      x.beginPath(); x.moveTo(0,y); x.lineTo(W,y); x.stroke();
    }
    /* packets travelling toward viewer along lanes */
    packets.forEach(function(k){
      k.z+=k.s*pace; if(k.z>1){k.z=0;k.lane=Math.random()*2-1;}
      var p=Math.pow(k.z,2.4), y=hy+p*(H-hy), px=vx+k.lane*p*W*.9+k.lane*6;
      var len=4+p*40, a=Math.min(1,k.z*1.6)*.75;
      var lg=x.createLinearGradient(px,y-len,px,y);
      lg.addColorStop(0,'rgba('+k.c+',0)'); lg.addColorStop(1,'rgba('+k.c+','+a+')');
      x.strokeStyle=lg; x.lineWidth=1+p*2; x.beginPath(); x.moveTo(px,y-len); x.lineTo(px,y); x.stroke();
    });
    /* top fade so content reads */
    var tf=x.createLinearGradient(0,0,0,hy); tf.addColorStop(0,'rgba(5,6,10,1)'); tf.addColorStop(1,'rgba(5,6,10,0)'); x.fillStyle=tf; x.fillRect(0,0,W,hy);
    schedule();
  }
  document.addEventListener('visibilitychange',schedule);
  schedule();
})();
