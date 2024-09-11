import {
  HttpClient,
  HttpErrorResponse,
  HttpEventType,
} from '@angular/common/http';
import { Component, EventEmitter, OnInit, Output } from '@angular/core';
import { environment } from '../../environments/environment';

@Component({
  selector: 'app-upload',
  templateUrl: './upload.component.html',
  styleUrls: ['./upload.component.css'],
})
export class UploadComponent implements OnInit {
  constructor(private http: HttpClient) {}

  progress: number = 0;
  message: string = '';
  teamNumber: string = '';
  attention: boolean = false;

  uploadError: boolean = false;

  imageUrl: string | null = null;

  @Output() public onUploadFinished = new EventEmitter();

  ngOnInit(): void {}

  uploadFile = (files: any) => {
    if (files.length === 0) {
      return;
    }
    this.message = '';
    this.progress = 0;
    this.uploadError = false;

    let fileToUpload = <File>files[0];
    const formData = new FormData();
    formData.append('file', fileToUpload, fileToUpload.name);

    this.http
      .post(`${environment.apiUrl}/photo?teamNumber=${this.teamNumber}&attention=${this.attention}`,
        formData,
        {
          reportProgress: true,
          observe: 'events',
        }
      )
      .subscribe({
        next: (event: any) => {
          if (event.type === HttpEventType.UploadProgress)
            this.progress = Math.round((100 * event.loaded) / event.total!);
          else if (event.type === HttpEventType.Response) {
            this.message = 'Upload færdig';
            this.onUploadFinished.emit(event.body);
            this.imageUrl = event.body.imageUrl;
            this.teamNumber = "";
            this.attention = false;
            files = undefined;
          }
        },
        error: (err: HttpErrorResponse) => {
          console.error(err);
          this.progress = 0;
          this.message = 'Fejl i upload';
          this.uploadError = true;
        }
      });
  };
}
