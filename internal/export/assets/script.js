(function(){
  var input=document.querySelector('.filter');
  input&&input.addEventListener('input',function(){
    var q=input.value.toLowerCase().trim();
    document.querySelectorAll('.nav-group').forEach(function(g){
      var any=false;
      g.querySelectorAll('li').forEach(function(li){var hit=!q||li.textContent.toLowerCase().indexOf(q)>-1;li.style.display=hit?'':'none';any=any||hit;});
      var titleHit=!q||g.querySelector('.nav-title').textContent.toLowerCase().indexOf(q)>-1;
      g.style.display=(any||titleHit)?'':'none';
    });
  });
  document.querySelectorAll('.sidebar a').forEach(function(a){a.addEventListener('click',function(){document.body.classList.remove('nav-open')})});
  var links={};document.querySelectorAll('.nav-group li a').forEach(function(a){links[a.getAttribute('href').slice(1)]=a});
  if('IntersectionObserver' in window){
    var obs=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting&&links[e.target.id]){
      document.querySelectorAll('.nav-group a.active').forEach(function(x){x.classList.remove('active')});links[e.target.id].classList.add('active');}})},{rootMargin:'0px 0px -75% 0px'});
    Object.keys(links).forEach(function(id){var el=document.getElementById(id);el&&obs.observe(el)});
  }
})();
