/* The shared folder is one click away from every tab. */
(function () {
  'use strict';
  var LM = window.LM;
  var button = LM.$('shared-folder-btn');
  var message = LM.$('shared-folder-msg');
  button.addEventListener('click', function () {
    button.disabled = true;
    LM.api('POST', '/api/sync/open').then(function (result) {
      LM.showMsg(message, result.message, result.ok ? 'ok' : 'err');
    }, function (error) {
      LM.showMsg(message, LM.errText(error), 'err');
    }).then(function () { button.disabled = false; });
  });
}());
